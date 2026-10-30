package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iraosandeep/dispatch/internal/notification"
)

const apiKey = "dsp_test_key_0123456789"

type fakeProvider struct {
	mu    sync.Mutex
	sent  []notification.Rendered
	err   error
	delay time.Duration
}

func (f *fakeProvider) Name() string { return "fake" }

func (f *fakeProvider) Send(ctx context.Context, r notification.Rendered) error {
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, r)
	return nil
}

func newTestServer(t *testing.T, p *fakeProvider, mutate ...func(*Options)) *Server {
	t.Helper()
	opts := Options{
		APIKeys: []string{apiKey, "dsp_rotated_key_9876543210"},
		Service: notification.NewService(p, nil),
	}
	for _, m := range mutate {
		m(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type result struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) result {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return result{status: rec.Code, header: rec.Header(), body: m, raw: rec.Body.String()}
}

func authed(extra ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + apiKey, "Content-Type": "application/json"}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

func errCode(r result) string {
	e, _ := r.body["error"].(map[string]any)
	s, _ := e["code"].(string)
	return s
}

func TestNotifySuccess(t *testing.T) {
	p := &fakeProvider{}
	s := newTestServer(t, p)
	r := do(t, s.Handler(), "POST", "/notify", `{"message":"Backup successful","title":"Backup Job","priority":"low"}`, authed())

	if r.status != 200 || r.body["ok"] != true || r.body["message"] != "Notification accepted" {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
	if len(p.sent) != 1 || p.sent[0].Text != "Backup Job\n\nBackup successful" || !p.sent[0].Silent {
		t.Errorf("sent = %+v", p.sent)
	}
	if r.header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Errorf("content-type = %q", r.header.Get("Content-Type"))
	}
}

func TestAuth(t *testing.T) {
	s := newTestServer(t, &fakeProvider{})
	body := `{"message":"x"}`
	tests := []struct {
		name   string
		auth   string
		status int
	}{
		{"missing", "", 401},
		{"wrong key", "Bearer nope", 401},
		{"wrong scheme", "Basic " + apiKey, 401},
		{"empty bearer", "Bearer ", 401},
		{"primary key", "Bearer " + apiKey, 200},
		{"rotated key", "Bearer dsp_rotated_key_9876543210", 200},
		{"case-insensitive scheme", "bearer " + apiKey, 200},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := map[string]string{"Content-Type": "application/json"}
			if tt.auth != "" {
				h["Authorization"] = tt.auth
			}
			r := do(t, s.Handler(), "POST", "/notify", body, h)
			if r.status != tt.status {
				t.Fatalf("status = %d; want %d (%s)", r.status, tt.status, r.raw)
			}
			if tt.status == 401 {
				if errCode(r) != "UNAUTHORIZED" || r.header.Get("WWW-Authenticate") == "" {
					t.Errorf("bad 401 response: %s %v", r.raw, r.header)
				}
			}
		})
	}
}

func TestNotifyRequestErrors(t *testing.T) {
	s := newTestServer(t, &fakeProvider{}, func(o *Options) { o.MaxBodyBytes = 1024 })
	tests := []struct {
		name    string
		method  string
		body    string
		headers map[string]string
		status  int
		code    string
	}{
		{"GET", "GET", "", authed(), 405, "METHOD_NOT_ALLOWED"},
		{"PUT", "PUT", `{"message":"x"}`, authed(), 405, "METHOD_NOT_ALLOWED"},
		{"no content type", "POST", `{"message":"x"}`, map[string]string{"Authorization": "Bearer " + apiKey}, 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"text content type", "POST", `{"message":"x"}`, authed("Content-Type", "text/plain"), 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"malformed", "POST", `{"message":`, authed(), 400, "INVALID_REQUEST"},
		{"empty body", "POST", ``, authed(), 400, "INVALID_REQUEST"},
		{"wrong type", "POST", `{"message":5}`, authed(), 400, "INVALID_REQUEST"},
		{"unknown field", "POST", `{"message":"x","bot_token":"y"}`, authed(), 400, "INVALID_REQUEST"},
		{"trailing data", "POST", `{"message":"x"}{"message":"y"}`, authed(), 400, "INVALID_REQUEST"},
		{"missing message", "POST", `{"title":"x"}`, authed(), 400, "EMPTY_MESSAGE"},
		{"bad priority", "POST", `{"message":"x","priority":"urgent"}`, authed(), 400, "INVALID_REQUEST"},
		{"body too large", "POST", `{"message":"` + strings.Repeat("a", 2000) + `"}`, authed(), 413, "PAYLOAD_TOO_LARGE"},
		{"charset ok", "POST", `{"message":"x"}`, authed("Content-Type", "application/json; charset=utf-8"), 200, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := do(t, s.Handler(), tt.method, "/notify", tt.body, tt.headers)
			if r.status != tt.status || errCode(r) != tt.code {
				t.Fatalf("got %d %s; want %d %s", r.status, r.raw, tt.status, tt.code)
			}
			if tt.status >= 400 && r.body["ok"] != false {
				t.Errorf("error response must have ok=false: %s", r.raw)
			}
			if tt.status == 405 && r.header.Get("Allow") != "POST" {
				t.Errorf("Allow = %q", r.header.Get("Allow"))
			}
		})
	}
}

func TestMessageTooLarge(t *testing.T) {
	s := newTestServer(t, &fakeProvider{})
	r := do(t, s.Handler(), "POST", "/notify", `{"message":"`+strings.Repeat("a", 4097)+`"}`, authed())
	if r.status != 413 || errCode(r) != "MESSAGE_TOO_LARGE" {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
}

func TestDeliveryErrorMapping(t *testing.T) {
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{&notification.Error{Code: notification.CodeRateLimited, Message: "slow", RetryAfter: 1500 * time.Millisecond}, 429, "RATE_LIMITED"},
		{notification.Errorf(notification.CodeTimeout, "t"), 504, "TIMEOUT"},
		{notification.Errorf(notification.CodeNetwork, "n"), 502, "NETWORK_ERROR"},
		{notification.Errorf(notification.CodeInvalidCredentials, "c"), 502, "INVALID_CREDENTIALS"},
		{notification.Errorf(notification.CodeInvalidDestination, "d"), 502, "INVALID_DESTINATION"},
		{notification.Errorf(notification.CodeDeliveryFailed, "f"), 502, "DELIVERY_FAILED"},
		{errors.New("raw internal detail"), 502, "DELIVERY_FAILED"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			s := newTestServer(t, &fakeProvider{err: tt.err})
			r := do(t, s.Handler(), "POST", "/notify", `{"message":"x"}`, authed())
			if r.status != tt.status || errCode(r) != tt.code {
				t.Fatalf("got %d %s; want %d %s", r.status, r.raw, tt.status, tt.code)
			}
			if tt.code == "RATE_LIMITED" && r.header.Get("Retry-After") != "2" {
				t.Errorf("Retry-After = %q; want 2", r.header.Get("Retry-After"))
			}
			if strings.Contains(r.raw, "raw internal detail") {
				t.Error("uncategorized internal errors must not be exposed")
			}
		})
	}
}

func TestRateLimit(t *testing.T) {
	s := newTestServer(t, &fakeProvider{}, func(o *Options) { o.RateLimit = 0.001; o.RateBurst = 2 })
	for i := 0; i < 2; i++ {
		if r := do(t, s.Handler(), "POST", "/notify", `{"message":"x"}`, authed()); r.status != 200 {
			t.Fatalf("request %d: %d", i, r.status)
		}
	}
	r := do(t, s.Handler(), "POST", "/notify", `{"message":"x"}`, authed())
	if r.status != 429 || errCode(r) != "RATE_LIMITED" || r.header.Get("Retry-After") == "" {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
	// Unauthenticated requests are rejected before consuming rate-limit tokens.
	if r := do(t, s.Handler(), "POST", "/notify", `{"message":"x"}`, nil); r.status != 401 {
		t.Errorf("unauthenticated got %d", r.status)
	}
}

func TestTokenBucketRefills(t *testing.T) {
	now := time.Unix(0, 0)
	b := newTokenBucket(2, 1)
	b.now = func() time.Time { return now }
	b.last = now
	if _, ok := b.take(); !ok {
		t.Fatal("first take should succeed")
	}
	if wait, ok := b.take(); ok || wait != 500*time.Millisecond {
		t.Fatalf("second take: ok=%v wait=%s", ok, wait)
	}
	now = now.Add(500 * time.Millisecond)
	if _, ok := b.take(); !ok {
		t.Fatal("bucket should have refilled")
	}
}

func TestHealthEndpoints(t *testing.T) {
	p := &fakeProvider{}
	ready := errors.New("missing DISPATCH_CHAT_ID")
	s := newTestServer(t, p, func(o *Options) { o.Ready = func() error { return ready } })

	if r := do(t, s.Handler(), "GET", "/healthz", "", nil); r.status != 200 || r.body["ok"] != true {
		t.Errorf("healthz: %d %s", r.status, r.raw)
	}
	r := do(t, s.Handler(), "GET", "/readyz", "", nil)
	if r.status != 503 || errCode(r) != "NOT_READY" {
		t.Errorf("readyz not ready: %d %s", r.status, r.raw)
	}
	ready = nil
	if r := do(t, s.Handler(), "GET", "/readyz", "", nil); r.status != 200 {
		t.Errorf("readyz ready: %d %s", r.status, r.raw)
	}
	if r := do(t, s.Handler(), "POST", "/healthz", "", nil); r.status != 405 {
		t.Errorf("POST /healthz: %d", r.status)
	}
	if r := do(t, s.Handler(), "GET", "/nope", "", nil); r.status != 404 || errCode(r) != "NOT_FOUND" {
		t.Errorf("404: %d %s", r.status, r.raw)
	}
	if len(p.sent) != 0 {
		t.Error("health endpoints must not send notifications")
	}
}

func TestNewRequiresAPIKey(t *testing.T) {
	if _, err := New(Options{Service: notification.NewService(&fakeProvider{}, nil)}); err == nil {
		t.Fatal("server must refuse to start without an API key")
	}
}

func TestGracefulShutdown(t *testing.T) {
	p := &fakeProvider{delay: 300 * time.Millisecond}
	s := newTestServer(t, p)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.Serve(ctx, ln) }()

	url := "http://" + ln.Addr().String() + "/notify"
	respCh := make(chan *http.Response, 1)
	go func() {
		req, _ := http.NewRequest("POST", url, strings.NewReader(`{"message":"in flight"}`))
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			respCh <- nil
			return
		}
		respCh <- resp
	}()

	time.Sleep(100 * time.Millisecond) // request is now in flight
	cancel()

	resp := <-respCh
	if resp == nil {
		t.FailNow()
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("in-flight request status = %d; want 200", resp.StatusCode)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down")
	}
	if _, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		t.Error("listener still accepting after shutdown")
	}
}

type formPart struct {
	name, filename, content string
}

func multipartBody(t *testing.T, parts ...formPart) (string, string) {
	t.Helper()
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.filename != "" {
			w, err = mw.CreateFormFile(p.name, p.filename)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, p.content)
	}
	mw.Close()
	return buf.String(), mw.FormDataContentType()
}

func TestNotifyMultipartUpload(t *testing.T) {
	p := &fakeProvider{}
	s := newTestServer(t, p)
	body, ct := multipartBody(t,
		formPart{name: "message", content: "Monthly report"},
		formPart{name: "priority", content: "low"},
		formPart{name: "file", filename: "report.csv", content: "a,b\n1,2\n"},
	)
	r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct))
	if r.status != 200 {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
	got := p.sent[0]
	if got.Text != "Monthly report" || !got.Silent || got.Attachment == nil ||
		got.Attachment.Name != "report.csv" || string(got.Attachment.Data) != "a,b\n1,2\n" {
		t.Errorf("sent %+v", got)
	}
}

func TestNotifyMultipartFileOnlyAndTextOnly(t *testing.T) {
	p := &fakeProvider{}
	s := newTestServer(t, p)

	body, ct := multipartBody(t, formPart{name: "file", filename: "x.log", content: "data"})
	if r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct)); r.status != 200 {
		t.Fatalf("file only: %d %s", r.status, r.raw)
	}
	body, ct = multipartBody(t, formPart{name: "message", content: "plain"})
	if r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct)); r.status != 200 {
		t.Fatalf("text only: %d %s", r.status, r.raw)
	}
	if p.sent[1].Attachment != nil || p.sent[1].Text != "plain" {
		t.Errorf("text-only multipart sent %+v", p.sent[1])
	}
}

func TestNotifyMultipartErrors(t *testing.T) {
	s := newTestServer(t, &fakeProvider{}, func(o *Options) { o.MaxUploadBytes = 2048 })
	tests := []struct {
		name   string
		parts  []formPart
		status int
		code   string
	}{
		{"empty", nil, 400, "EMPTY_MESSAGE"},
		{"unknown field", []formPart{{name: "chat_id", content: "1"}}, 400, "INVALID_REQUEST"},
		{"two files", []formPart{{name: "file", filename: "a", content: "1"}, {name: "file", filename: "b", content: "2"}}, 400, "INVALID_REQUEST"},
		{"file in text field", []formPart{{name: "message", filename: "m.txt", content: "x"}}, 400, "INVALID_REQUEST"},
		{"empty file", []formPart{{name: "file", filename: "e.txt", content: ""}}, 400, "INVALID_REQUEST"},
		{"caption too long", []formPart{{name: "message", content: strings.Repeat("a", 1025)}, {name: "file", filename: "a", content: "1"}}, 413, "MESSAGE_TOO_LARGE"},
		{"body too large", []formPart{{name: "file", filename: "big", content: strings.Repeat("x", 4096)}}, 413, "PAYLOAD_TOO_LARGE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, ct := multipartBody(t, tt.parts...)
			r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct))
			if r.status != tt.status || errCode(r) != tt.code {
				t.Fatalf("got %d %s; want %d %s", r.status, r.raw, tt.status, tt.code)
			}
		})
	}
}

func TestNotifyMultipartRequiresAuth(t *testing.T) {
	p := &fakeProvider{}
	s := newTestServer(t, p)
	body, ct := multipartBody(t, formPart{name: "file", filename: "x", content: "1"})
	if r := do(t, s.Handler(), "POST", "/notify", body, map[string]string{"Content-Type": ct}); r.status != 401 {
		t.Fatalf("got %d", r.status)
	}
	if len(p.sent) != 0 {
		t.Error("unauthenticated upload was sent")
	}
}

func TestConcurrentUploadLimit(t *testing.T) {
	s := newTestServer(t, &fakeProvider{}, func(o *Options) { o.MaxConcurrentUploads = 1 })
	s.uploadSem <- struct{}{} // simulate one upload in progress

	body, ct := multipartBody(t, formPart{name: "file", filename: "x", content: "1"})
	r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct))
	if r.status != 429 || errCode(r) != "RATE_LIMITED" || r.header.Get("Retry-After") == "" {
		t.Fatalf("got %d %s", r.status, r.raw)
	}
	// JSON requests are not affected by the upload limit.
	if r := do(t, s.Handler(), "POST", "/notify", `{"message":"x"}`, authed()); r.status != 200 {
		t.Fatalf("json got %d %s", r.status, r.raw)
	}

	<-s.uploadSem
	if r := do(t, s.Handler(), "POST", "/notify", body, authed("Content-Type", ct)); r.status != 200 {
		t.Fatalf("after release got %d %s", r.status, r.raw)
	}
}
