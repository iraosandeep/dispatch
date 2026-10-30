package telegram_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/iraosandeep/dispatch/internal/notification"
	"github.com/iraosandeep/dispatch/internal/telegram"
	"github.com/iraosandeep/dispatch/internal/telegram/telegramtest"
)

const token = "123456:TEST-secret-token-value"

func newClient(baseURL string, opts telegram.Options) *telegram.Client {
	opts.BaseURL = baseURL
	if opts.BaseBackoff == 0 {
		opts.BaseBackoff = time.Millisecond
	}
	if opts.MaxBackoff == 0 {
		opts.MaxBackoff = 5 * time.Millisecond
	}
	return telegram.New(token, opts)
}

func send(t *testing.T, srv *telegramtest.Server, opts telegram.Options, msg notification.Rendered) error {
	t.Helper()
	p := telegram.NewProvider(newClient(srv.URL, opts), "-1001")
	return p.Send(context.Background(), msg)
}

func TestSendSuccess(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	if err := send(t, srv, telegram.Options{}, notification.Rendered{Text: "hello <b>", Silent: true}); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests("sendMessage")
	if len(reqs) != 1 {
		t.Fatalf("got %d requests", len(reqs))
	}
	b := reqs[0].Body
	if b["chat_id"] != "-1001" || b["text"] != "hello <b>" || b["disable_notification"] != true {
		t.Errorf("unexpected body %v", b)
	}
	if _, ok := b["parse_mode"]; ok {
		t.Error("parse_mode must not be set: messages are plain text")
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name     string
		resp     telegramtest.Response
		code     notification.Code
		attempts int
		contains string
	}{
		{"chat not found", telegramtest.Response{Status: 400, Body: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`},
			notification.CodeInvalidDestination, 1, "DISPATCH_CHAT_ID"},
		{"blocked", telegramtest.Response{Status: 403, Body: `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`},
			notification.CodeInvalidDestination, 1, "blocked"},
		{"migrated", telegramtest.Response{Status: 400, Body: `{"ok":false,"error_code":400,"description":"Bad Request: group chat was upgraded","parameters":{"migrate_to_chat_id":-100777}}`},
			notification.CodeInvalidDestination, 1, "-100777"},
		{"too long", telegramtest.Response{Status: 400, Body: `{"ok":false,"error_code":400,"description":"Bad Request: message is too long"}`},
			notification.CodeMessageTooLarge, 1, "too long"},
		{"server error retried", telegramtest.Response{Status: 502, Body: `{"ok":false,"error_code":502,"description":"Bad Gateway"}`},
			notification.CodeDeliveryFailed, 3, "unavailable"},
		{"non-json 5xx retried", telegramtest.Response{Status: 503, Body: `<html>down</html>`},
			notification.CodeDeliveryFailed, 3, "503"},
		{"long retry_after not retried", telegramtest.Response{Status: 429, Body: `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 120","parameters":{"retry_after":120}}`},
			notification.CodeRateLimited, 1, "2m0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := telegramtest.NewServer(t, token)
			srv.Respond("sendMessage", tt.resp)
			err := send(t, srv, telegram.Options{}, notification.Rendered{Text: "x"})
			if got := notification.CodeOf(err); got != tt.code {
				t.Fatalf("code = %v (%v); want %v", got, err, tt.code)
			}
			if !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("error %q should contain %q", err, tt.contains)
			}
			if n := len(srv.Requests("sendMessage")); n != tt.attempts {
				t.Errorf("attempts = %d; want %d", n, tt.attempts)
			}
		})
	}
}

func TestInvalidToken(t *testing.T) {
	srv := telegramtest.NewServer(t, "other-token")
	err := send(t, srv, telegram.Options{}, notification.Rendered{Text: "x"})
	if notification.CodeOf(err) != notification.CodeInvalidCredentials {
		t.Fatalf("got %v", err)
	}
}

func TestRetryAfterIsRespected(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	srv.Respond("sendMessage",
		telegramtest.Response{Status: 429, Body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":1}}`},
		telegramtest.Response{},
	)
	start := time.Now()
	if err := send(t, srv, telegram.Options{}, notification.Rendered{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("retried after %s; Telegram asked for 1s", elapsed)
	}
	if n := len(srv.Requests("sendMessage")); n != 2 {
		t.Errorf("attempts = %d; want 2", n)
	}
}

func TestRetryAfterBeyondDeadlineFailsFast(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	srv.Respond("sendMessage",
		telegramtest.Response{Status: 429, Body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":20}}`})
	p := telegram.NewProvider(newClient(srv.URL, telegram.Options{}), "1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	start := time.Now()
	err := p.Send(ctx, notification.Rendered{Text: "x"})
	var nerr *notification.Error
	if !errors.As(err, &nerr) || nerr.Code != notification.CodeRateLimited || nerr.RetryAfter != 20*time.Second {
		t.Fatalf("got %#v", err)
	}
	if time.Since(start) > time.Second {
		t.Error("should not wait when retry_after exceeds the deadline")
	}
}

func TestTransientThenSuccess(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	srv.Respond("sendMessage", telegramtest.Response{Status: 500, Body: `{"ok":false,"error_code":500,"description":"Internal"}`}, telegramtest.Response{})
	if err := send(t, srv, telegram.Options{}, notification.Rendered{Text: "x"}); err != nil {
		t.Fatal(err)
	}
}

func TestTimeoutDoesNotLeakToken(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer slow.Close()

	p := telegram.NewProvider(newClient(slow.URL, telegram.Options{
		HTTPClient:  &http.Client{Timeout: 50 * time.Millisecond},
		MaxAttempts: 2,
	}), "1")
	err := p.Send(context.Background(), notification.Rendered{Text: "x"})
	if notification.CodeOf(err) != notification.CodeTimeout {
		t.Fatalf("got %v", err)
	}
	assertNoToken(t, err)
}

func TestNetworkErrorDoesNotLeakToken(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // connection refused

	p := telegram.NewProvider(newClient(url, telegram.Options{MaxAttempts: 2}), "1")
	err := p.Send(context.Background(), notification.Rendered{Text: "x"})
	if notification.CodeOf(err) != notification.CodeNetwork {
		t.Fatalf("got %v", err)
	}
	assertNoToken(t, err)
}

func TestDNSFailure(t *testing.T) {
	p := telegram.NewProvider(newClient("http://dispatch-test.invalid", telegram.Options{MaxAttempts: 1}), "1")
	err := p.Send(context.Background(), notification.Rendered{Text: "x"})
	if notification.CodeOf(err) != notification.CodeNetwork || !strings.Contains(err.Error(), "resolve") {
		t.Fatalf("got %v", err)
	}
	assertNoToken(t, err)
}

func TestContextCancelStopsRetries(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	srv.Respond("sendMessage", telegramtest.Response{Status: 500, Body: `{"ok":false,"error_code":500,"description":"x"}`})
	c := newClient(srv.URL, telegram.Options{MaxAttempts: 10, BaseBackoff: time.Second, MaxBackoff: time.Second})
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_ = c.SendMessage(ctx, telegram.SendMessageRequest{ChatID: "1", Text: "x"})
	if time.Since(start) > 3*time.Second {
		t.Error("retries should stop at the context deadline")
	}
	if n := len(srv.Requests("sendMessage")); n >= 10 {
		t.Errorf("made %d attempts despite deadline", n)
	}
}

func TestGetMeAndUpdates(t *testing.T) {
	srv := telegramtest.NewServer(t, token)
	srv.Respond("getUpdates", telegramtest.Response{Body: `{"ok":true,"result":[
		{"update_id":1,"message":{"chat":{"id":111,"type":"private","first_name":"Ada"}}},
		{"update_id":2,"my_chat_member":{"chat":{"id":-222,"type":"group","title":"Ops"}}}]}`})
	c := newClient(srv.URL, telegram.Options{})

	me, err := c.GetMe(context.Background())
	if err != nil || me.Username != "dispatch_test_bot" {
		t.Fatalf("GetMe = %+v, %v", me, err)
	}
	ups, err := c.GetUpdates(context.Background())
	if err != nil || len(ups) != 2 {
		t.Fatalf("GetUpdates = %v, %v", ups, err)
	}
	c1, _ := ups[0].Chat()
	c2, _ := ups[1].Chat()
	if c1.ID != 111 || c1.DisplayName() != "Ada" || c2.ID != -222 || c2.DisplayName() != "Ops" {
		t.Errorf("chats = %+v %+v", c1, c2)
	}
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	for e := err; e != nil; e = errors.Unwrap(e) {
		if strings.Contains(e.Error(), token) {
			t.Fatalf("error chain leaks bot token: %q", e.Error())
		}
	}
}
