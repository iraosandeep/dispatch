package notification

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestParsePriority(t *testing.T) {
	cases := map[string]Priority{"": PriorityNormal, "LOW": PriorityLow, " high ": PriorityHigh, "normal": PriorityNormal}
	for in, want := range cases {
		got, err := ParsePriority(in)
		if err != nil || got != want {
			t.Errorf("ParsePriority(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParsePriority("urgent"); CodeOf(err) != CodeInvalidRequest {
		t.Errorf("ParsePriority(urgent) error code = %v", CodeOf(err))
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		text string
		mute bool
	}{
		{"plain", Message{Body: "done"}, "done", false},
		{"title", Message{Title: "Backup", Body: "done"}, "Backup\n\ndone", false},
		{"high with title", Message{Title: "Import Failed", Body: "boom", Priority: PriorityHigh}, "🚨 Import Failed\n\nboom", false},
		{"emoji no title", Message{Body: "ok", Emoji: "✅"}, "✅ ok", false},
		{"high emoji no title", Message{Body: "x", Emoji: "🔥", Priority: PriorityHigh}, "🚨 🔥 x", false},
		{"low is silent", Message{Body: "fyi", Priority: PriorityLow}, "fyi", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := tt.msg.Render()
			if r.Text != tt.text || r.Silent != tt.mute {
				t.Errorf("Render() = %q silent=%v; want %q silent=%v", r.Text, r.Silent, tt.text, tt.mute)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		code Code // "" means valid
	}{
		{"ok", Message{Body: "hi"}, ""},
		{"empty", Message{Body: ""}, CodeEmptyMessage},
		{"whitespace", Message{Body: " \n\t"}, CodeEmptyMessage},
		{"multiline title", Message{Body: "x", Title: "a\nb"}, CodeInvalidRequest},
		{"bad priority", Message{Body: "x", Priority: "urgent"}, CodeInvalidRequest},
		{"long title", Message{Body: "x", Title: strings.Repeat("t", MaxTitleLength+1)}, CodeInvalidRequest},
		{"emoji whitespace", Message{Body: "x", Emoji: "a b"}, CodeInvalidRequest},
		{"invalid utf8", Message{Body: "\xff"}, CodeInvalidRequest},
		{"exact limit", Message{Body: strings.Repeat("a", MaxTextLength)}, ""},
		{"over limit", Message{Body: strings.Repeat("a", MaxTextLength+1)}, CodeMessageTooLarge},
		// Each 😀 is two UTF-16 code units, as Telegram counts them.
		{"emoji over limit", Message{Body: strings.Repeat("😀", MaxTextLength/2+1)}, CodeMessageTooLarge},
		{"title pushes over", Message{Title: "T", Body: strings.Repeat("a", MaxTextLength-2)}, CodeMessageTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if tt.code == "" {
				if err != nil {
					t.Fatalf("Validate() = %v; want nil", err)
				}
				return
			}
			if CodeOf(err) != tt.code {
				t.Fatalf("Validate() code = %v (%v); want %v", CodeOf(err), err, tt.code)
			}
		})
	}
}

func TestTooLargeMessageMentionsNoTruncation(t *testing.T) {
	err := Message{Body: strings.Repeat("a", 5000)}.Validate()
	if !strings.Contains(err.Error(), "5000") || !strings.Contains(err.Error(), "never truncates") {
		t.Errorf("error should report size and no truncation: %v", err)
	}
}

type fakeProvider struct {
	got Rendered
	err error
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Send(_ context.Context, r Rendered) error {
	f.got = r
	return f.err
}

func TestServiceNotify(t *testing.T) {
	p := &fakeProvider{}
	svc := NewService(p, nil)
	if err := svc.Notify(context.Background(), Message{Title: "T", Body: "B"}); err != nil {
		t.Fatal(err)
	}
	if p.got.Text != "T\n\nB" {
		t.Errorf("provider got %q", p.got.Text)
	}

	p.err = Errorf(CodeRateLimited, "slow down")
	if err := svc.Notify(context.Background(), Message{Body: "x"}); CodeOf(err) != CodeRateLimited {
		t.Errorf("Notify error = %v", err)
	}
}

func TestServiceValidatesBeforeSending(t *testing.T) {
	p := &fakeProvider{}
	err := NewService(p, nil).Notify(context.Background(), Message{Body: ""})
	if CodeOf(err) != CodeEmptyMessage || p.got.Text != "" {
		t.Errorf("empty message should be rejected without sending: err=%v sent=%q", err, p.got.Text)
	}
}

func TestServiceWithoutProvider(t *testing.T) {
	err := NewService(nil, nil).Notify(context.Background(), Message{Body: "x"})
	if CodeOf(err) != CodeNotConfigured {
		t.Errorf("got %v", err)
	}
}

func TestCodeOfUncategorized(t *testing.T) {
	if CodeOf(errors.New("boom")) != CodeDeliveryFailed {
		t.Error("uncategorized errors should map to DELIVERY_FAILED")
	}
}

func TestAttachmentValidation(t *testing.T) {
	file := &Attachment{Name: "report.pdf", Data: []byte("%PDF")}
	tests := []struct {
		name string
		msg  Message
		code Code
	}{
		{"file only", Message{Attachment: file}, ""},
		{"file with caption", Message{Body: "Monthly report", Attachment: file}, ""},
		{"caption at limit", Message{Body: strings.Repeat("a", MaxCaptionLength), Attachment: file}, ""},
		{"caption over limit", Message{Body: strings.Repeat("a", MaxCaptionLength+1), Attachment: file}, CodeMessageTooLarge},
		{"empty file", Message{Attachment: &Attachment{Name: "x.txt"}}, CodeInvalidRequest},
		{"path in name", Message{Attachment: &Attachment{Name: "../x", Data: []byte("1")}}, CodeInvalidRequest},
		{"too large", Message{Attachment: &Attachment{Name: "big.bin", Data: make([]byte, MaxAttachmentBytes+1)}}, CodeAttachmentTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.msg.Validate()
			if tt.code == "" && err != nil || tt.code != "" && CodeOf(err) != tt.code {
				t.Fatalf("Validate() = %v; want code %q", err, tt.code)
			}
		})
	}
}

func TestRenderAttachmentCaption(t *testing.T) {
	file := &Attachment{Name: "a.txt", Data: []byte("x")}
	cases := []struct {
		msg  Message
		want string
	}{
		{Message{Attachment: file}, ""},
		{Message{Title: "Monthly report", Attachment: file}, "Monthly report"},
		{Message{Emoji: "📊", Priority: PriorityHigh, Attachment: file}, "🚨 📊"},
		{Message{Title: "T", Body: "B", Attachment: file}, "T\n\nB"},
	}
	for _, c := range cases {
		r := c.msg.Render()
		if r.Text != c.want || r.Attachment != file {
			t.Errorf("Render(%+v) = %q; want %q", c.msg, r.Text, c.want)
		}
	}
}
