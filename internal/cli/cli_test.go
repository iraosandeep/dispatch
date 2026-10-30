package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/telegram/telegramtest"
)

const botToken = "123456:CLI-test-secret-token"

type harness struct {
	tg     *telegramtest.Server
	env    map[string]string
	stdin  string
	tty    bool
	stdout bytes.Buffer
	stderr bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	tg := telegramtest.NewServer(t, botToken)
	return &harness{
		tg: tg,
		env: map[string]string{
			"XDG_CONFIG_HOME":        t.TempDir(),
			config.EnvBotToken:       botToken,
			config.EnvChatID:         "-100123",
			config.EnvTelegramAPIURL: tg.URL,
		},
	}
}

func (h *harness) run(args ...string) int {
	h.stdout.Reset()
	h.stderr.Reset()
	app := &App{
		Stdin:      strings.NewReader(h.stdin),
		Stdout:     &h.stdout,
		Stderr:     &h.stderr,
		Getenv:     func(k string) string { return h.env[k] },
		Build:      BuildInfo{Version: "test", Commit: "abc", Date: "today"},
		IsTerminal: func() bool { return h.tty },
	}
	return app.Run(context.Background(), args)
}

func (h *harness) sentTexts() []string {
	var out []string
	for _, r := range h.tg.Requests("sendMessage") {
		out = append(out, r.Body["text"].(string))
	}
	return out
}

func TestSendArgument(t *testing.T) {
	h := newHarness(t)
	if code := h.run("send", "Backup successful"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.sentTexts(); len(got) != 1 || got[0] != "Backup successful" {
		t.Errorf("sent %q", got)
	}
	reqs := h.tg.Requests("sendMessage")
	if reqs[0].Body["chat_id"] != "-100123" {
		t.Errorf("chat_id = %v", reqs[0].Body["chat_id"])
	}
	if !strings.Contains(h.stderr.String(), "accepted") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestSendUnquotedWordsAndQuiet(t *testing.T) {
	h := newHarness(t)
	if code := h.run("send", "-q", "Import", "done"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.sentTexts(); got[0] != "Import done" {
		t.Errorf("sent %q", got)
	}
	if h.stderr.Len() != 0 || h.stdout.Len() != 0 {
		t.Errorf("--quiet printed output: %q %q", h.stdout.String(), h.stderr.String())
	}
}

func TestSendTitlePriorityEmoji(t *testing.T) {
	h := newHarness(t)
	code := h.run("send", "--title", "Import Failed", "--priority", "high", "--emoji", "🔥", "The accounting import failed")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.sentTexts()[0]; got != "🚨 🔥 Import Failed\n\nThe accounting import failed" {
		t.Errorf("sent %q", got)
	}

	h.run("send", "--priority", "low", "fyi")
	if h.tg.Requests("sendMessage")[1].Body["disable_notification"] != true {
		t.Error("low priority should be silent")
	}
}

func TestSendStdin(t *testing.T) {
	h := newHarness(t)
	h.stdin = "Deployment succeeded\n"
	if code := h.run("send", "--stdin"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.sentTexts()[0]; got != "Deployment succeeded" {
		t.Errorf("sent %q", got)
	}
}

func TestSendFile(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "report.txt")
	os.WriteFile(path, []byte("line 1\nline 2\n"), 0o600)
	if code := h.run("send", "--file", path); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.sentTexts()[0]; got != "line 1\nline 2" {
		t.Errorf("sent %q", got)
	}
}

func TestSendInputErrors(t *testing.T) {
	big := filepath.Join(t.TempDir(), "big.txt")
	os.WriteFile(big, []byte(strings.Repeat("a", 5000)), 0o600)

	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"no message", []string{"send"}, "", "no message provided"},
		{"two sources", []string{"send", "--stdin", "hello"}, "x", "only one message source"},
		{"empty stdin", []string{"send", "--stdin"}, "\n", "empty"},
		{"blank arg", []string{"send", "   "}, "", "empty"},
		{"missing file", []string{"send", "--file", "/no/such/file"}, "", "file not found"},
		{"too long", []string{"send", "--file", big}, "", "never truncates"},
		{"bad priority", []string{"send", "--priority", "urgent", "x"}, "", "invalid priority"},
		{"unknown flag", []string{"send", "--nope", "x"}, "", "unknown flag"},
		{"unknown command", []string{"sned", "x"}, "", "unknown command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.stdin = tt.stdin
			if code := h.run(tt.args...); code != ExitUsage {
				t.Fatalf("exit %d; want %d (%s)", code, ExitUsage, h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), tt.want) {
				t.Errorf("stderr %q should contain %q", h.stderr.String(), tt.want)
			}
			if n := len(h.tg.Requests("")); n != 0 {
				t.Errorf("invalid input made %d Telegram calls", n)
			}
		})
	}
}

func TestSendExitCodes(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*harness)
		code  int
	}{
		{"missing config", func(h *harness) { delete(h.env, config.EnvBotToken) }, ExitConfig},
		{"invalid token", func(h *harness) { h.env[config.EnvBotToken] = "999:wrong" }, ExitConfig},
		{"chat not found", func(h *harness) {
			h.tg.Respond("sendMessage", telegramtest.Response{Status: 400, Body: `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`})
		}, ExitConfig},
		{"rate limited", func(h *harness) {
			h.tg.Respond("sendMessage", telegramtest.Response{Status: 429, Body: `{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":300}}`})
		}, ExitTransient},
		{"rejected", func(h *harness) {
			h.tg.Respond("sendMessage", telegramtest.Response{Status: 400, Body: `{"ok":false,"error_code":400,"description":"Bad Request: something else"}`})
		}, ExitFailure},
		{"bad config file", func(h *harness) {
			p := filepath.Join(h.env["XDG_CONFIG_HOME"], "dispatch", "config.json")
			os.MkdirAll(filepath.Dir(p), 0o700)
			os.WriteFile(p, []byte("{not json"), 0o600)
		}, ExitConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.setup(h)
			if code := h.run("send", "hello"); code != tt.code {
				t.Fatalf("exit %d; want %d (%s)", code, tt.code, h.stderr.String())
			}
			if strings.Contains(h.stderr.String(), botToken) || strings.Contains(h.stderr.String(), "999:wrong") {
				t.Errorf("stderr leaks token: %s", h.stderr.String())
			}
		})
	}
}

func TestChatIDFlagOverridesEnv(t *testing.T) {
	h := newHarness(t)
	if code := h.run("send", "--chat-id", "777", "x"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if got := h.tg.Requests("sendMessage")[0].Body["chat_id"]; got != "777" {
		t.Errorf("chat_id = %v", got)
	}
}

func TestTestCommand(t *testing.T) {
	h := newHarness(t)
	if code := h.run("test"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if len(h.tg.Requests("getMe")) != 1 {
		t.Error("test should validate the token with getMe")
	}
	if got := h.sentTexts(); len(got) != 1 || !strings.Contains(got[0], "Dispatch test") {
		t.Errorf("sent %q", got)
	}
	if !strings.Contains(h.stdout.String(), "@dispatch_test_bot") {
		t.Errorf("stdout = %q", h.stdout.String())
	}
}

func TestConfigShowMasksSecrets(t *testing.T) {
	h := newHarness(t)
	h.env[config.EnvAPIKey] = "dsp_very_secret_api_key_value"
	if code := h.run("config", "show"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	out := h.stdout.String()
	if strings.Contains(out, botToken) || strings.Contains(out, "very_secret") {
		t.Fatalf("config show leaks secrets:\n%s", out)
	}
	for _, want := range []string{"********oken", "-100123", "env", "127.0.0.1:8787", "default"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestConfigGenKey(t *testing.T) {
	h := newHarness(t)
	h.run("config", "gen-key")
	k1 := strings.TrimSpace(h.stdout.String())
	h.run("config", "gen-key")
	k2 := strings.TrimSpace(h.stdout.String())
	if !strings.HasPrefix(k1, "dsp_") || len(k1) < 40 || k1 == k2 {
		t.Errorf("keys %q %q", k1, k2)
	}
}

func TestConfigInitRequiresTerminal(t *testing.T) {
	h := newHarness(t)
	if code := h.run("config", "init"); code != ExitConfig {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(h.stderr.String(), "DISPATCH_BOT_TOKEN") {
		t.Errorf("stderr = %q", h.stderr.String())
	}
}

func TestConfigInitInteractive(t *testing.T) {
	h := newHarness(t)
	delete(h.env, config.EnvBotToken)
	delete(h.env, config.EnvChatID)
	h.tty = true
	h.tg.Respond("getUpdates", telegramtest.Response{Body: `{"ok":true,"result":[
		{"update_id":1,"message":{"chat":{"id":555,"type":"private","first_name":"Ada"}}}]}`})
	h.stdin = strings.Join([]string{
		botToken, // bot token
		"",       // chat ID: blank → detect
		"",       // press Enter after messaging the bot
		"y",      // use detected chat
		"y",      // generate API key
		"n",      // skip test message
	}, "\n") + "\n"

	if code := h.run("config", "init"); code != ExitOK {
		t.Fatalf("exit %d\nstdout:%s\nstderr:%s", code, h.stdout.String(), h.stderr.String())
	}

	path := filepath.Join(h.env["XDG_CONFIG_HOME"], "dispatch", "config.json")
	cfg, err := config.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != botToken || cfg.ChatID != "555" || len(cfg.APIKeys) != 1 {
		t.Errorf("saved config = %+v", cfg)
	}
	if !strings.Contains(h.stdout.String(), "Connected to @dispatch_test_bot") {
		t.Errorf("stdout = %s", h.stdout.String())
	}

	// Config file alone is now sufficient to send.
	if code := h.run("send", "from file config"); code != ExitOK {
		t.Fatalf("send with saved config: exit %d: %s", code, h.stderr.String())
	}
}

func TestServeRefusesUnsafeConfig(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"no api key", []string{"serve"}, nil, "no API key"},
		{"remote without opt-in", []string{"serve", "--addr", "0.0.0.0:8787"},
			map[string]string{config.EnvAPIKey: "dsp_0123456789abcdef"}, "--allow-remote"},
		{"bad log format", []string{"serve", "--log-format", "xml"}, nil, "log-format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			for k, v := range tt.env {
				h.env[k] = v
			}
			if code := h.run(tt.args...); code == ExitOK {
				t.Fatal("serve should refuse to start")
			}
			if !strings.Contains(h.stderr.String(), tt.want) {
				t.Errorf("stderr %q should contain %q", h.stderr.String(), tt.want)
			}
		})
	}
}

func TestVersion(t *testing.T) {
	h := newHarness(t)
	h.run("version")
	if !strings.Contains(h.stdout.String(), "dispatch test (commit abc") {
		t.Errorf("version = %q", h.stdout.String())
	}
}

func TestSensitiveLogKeys(t *testing.T) {
	var buf bytes.Buffer
	newLogger(&buf, -4, "json").Info("x", "bot_token", "s1", "Authorization", "s2", "api_key", "s3", "chat_id", "ok")
	out := buf.String()
	if strings.Contains(out, "s1") || strings.Contains(out, "s2") || strings.Contains(out, "s3") || !strings.Contains(out, "ok") {
		t.Errorf("log = %s", out)
	}
}

func TestSendAttachment(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "report.csv")
	os.WriteFile(path, []byte("a,b\n1,2\n"), 0o600)

	if code := h.run("send", "--attach", path, "--title", "Monthly report", "See attached"); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	reqs := h.tg.Requests("sendDocument")
	if len(reqs) != 1 || len(h.tg.Requests("sendMessage")) != 0 {
		t.Fatalf("requests = %+v", h.tg.Requests(""))
	}
	if reqs[0].Body["caption"] != "Monthly report\n\nSee attached" {
		t.Errorf("caption = %q", reqs[0].Body["caption"])
	}
	if f := reqs[0].Files["document"]; f.Name != "report.csv" || string(f.Data) != "a,b\n1,2\n" {
		t.Errorf("file = %q %q", f.Name, f.Data)
	}
}

func TestSendAttachmentWithoutMessage(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "go.sum")
	os.WriteFile(path, []byte("checksums"), 0o600)
	if code := h.run("send", "-a", path); code != ExitOK {
		t.Fatalf("exit %d: %s", code, h.stderr.String())
	}
	if _, ok := h.tg.Requests("sendDocument")[0].Body["caption"]; ok {
		t.Error("no caption expected")
	}
}

func TestSendAttachmentErrors(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.txt")
	os.WriteFile(empty, nil, 0o600)
	small := filepath.Join(dir, "small.txt")
	os.WriteFile(small, []byte("x"), 0o600)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing", []string{"send", "--attach", filepath.Join(dir, "nope")}, "attachment not found"},
		{"directory", []string{"send", "--attach", dir}, "directory"},
		{"empty", []string{"send", "--attach", empty}, "empty"},
		{"caption too long", []string{"send", "--attach", small, strings.Repeat("a", 1025)}, "1024"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			if code := h.run(tt.args...); code != ExitUsage {
				t.Fatalf("exit %d; want %d (%s)", code, ExitUsage, h.stderr.String())
			}
			if !strings.Contains(h.stderr.String(), tt.want) {
				t.Errorf("stderr %q should contain %q", h.stderr.String(), tt.want)
			}
			if n := len(h.tg.Requests("")); n != 0 {
				t.Errorf("made %d Telegram calls", n)
			}
		})
	}
}
