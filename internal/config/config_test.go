package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaults(t *testing.T) {
	cfg, err := Load(LoadOptions{Getenv: env(map[string]string{"XDG_CONFIG_HOME": t.TempDir()})})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != DefaultAddr || cfg.Timeout.Std() != DefaultTimeout || cfg.RateLimit != DefaultRateLimit {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if cfg.SourceOf("addr") != "default" || cfg.SourceOf("bot_token") != "unset" {
		t.Errorf("sources = %v", cfg.Sources)
	}
}

func TestPrecedence(t *testing.T) {
	path := writeFile(t, `{"bot_token":"file-token","chat_id":"file-chat","addr":"127.0.0.1:1111","timeout":"5s","api_keys":["file-key-000000000"]}`, 0o600)
	getenv := env(map[string]string{
		EnvChatID:  "env-chat",
		EnvAddr:    "127.0.0.1:2222",
		EnvAPIKey:  "env-key-1111111111, env-key-2222222222 ,",
		EnvTimeout: "7s",
	})
	flagChat := "flag-chat"
	cfg, err := Load(LoadOptions{Path: path, Getenv: getenv, Overrides: Overrides{ChatID: &flagChat}})
	if err != nil {
		t.Fatal(err)
	}

	checks := []struct{ key, got, want, src string }{
		{"bot_token", cfg.BotToken, "file-token", "file"},
		{"chat_id", cfg.ChatID, "flag-chat", "flag"},
		{"addr", cfg.Addr, "127.0.0.1:2222", "env"},
		{"timeout", cfg.Timeout.Std().String(), "7s", "env"},
	}
	for _, c := range checks {
		if c.got != c.want || cfg.SourceOf(c.key) != c.src {
			t.Errorf("%s = %q from %s; want %q from %s", c.key, c.got, cfg.SourceOf(c.key), c.want, c.src)
		}
	}
	if len(cfg.APIKeys) != 2 || cfg.APIKeys[1] != "env-key-2222222222" {
		t.Errorf("api keys = %q", cfg.APIKeys)
	}
}

func TestEnvConfigPath(t *testing.T) {
	path := writeFile(t, `{"chat_id":"from-env-path"}`, 0o600)
	cfg, err := Load(LoadOptions{Getenv: env(map[string]string{EnvConfig: path})})
	if err != nil || cfg.ChatID != "from-env-path" {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
}

func TestExplicitMissingFileIsError(t *testing.T) {
	if _, err := Load(LoadOptions{Path: filepath.Join(t.TempDir(), "nope.json"), Getenv: env(nil)}); err == nil {
		t.Fatal("expected error for missing explicit config file")
	}
}

func TestInvalidValues(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		env  map[string]string
	}{
		"bad json":     {file: `{`},
		"bad duration": {file: `{"timeout":"soon"}`},
		"env timeout":  {file: `{}`, env: map[string]string{EnvTimeout: "-1s"}},
		"env bool":     {file: `{}`, env: map[string]string{EnvAllowRemote: "maybe"}},
		"env rate":     {file: `{}`, env: map[string]string{EnvRateLimit: "fast"}},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, tc.file, 0o600)
			if _, err := Load(LoadOptions{Path: path, Getenv: env(tc.env)}); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestInsecurePermissionsWarning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path := writeFile(t, `{"bot_token":"secret"}`, 0o644)
	cfg, err := Load(LoadOptions{Path: path, Getenv: env(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "chmod 600") {
		t.Errorf("warnings = %v", cfg.Warnings)
	}
}

func TestSaveRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	in := &Config{BotToken: "tok", ChatID: "42", APIKeys: []string{"k"}, Timeout: Duration(10 * time.Second)}
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0o600 {
			t.Errorf("file mode = %o; want 600", info.Mode().Perm())
		}
		dir, _ := os.Stat(filepath.Dir(path))
		if dir.Mode().Perm() != 0o700 {
			t.Errorf("dir mode = %o; want 700", dir.Mode().Perm())
		}
	}
	out, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.BotToken != "tok" || out.ChatID != "42" || out.Timeout.Std() != 10*time.Second {
		t.Errorf("round trip = %+v", out)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                    "",
		"short":               "********",
		"123456:ABCDEFGHWXYZ": "********WXYZ",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q; want %q", in, got, want)
		}
	}
	c := &Config{BotToken: "123456:ABCDEFGHWXYZ", APIKeys: []string{"dsp_supersecretvalue"}}
	r := c.Redacted()
	if strings.Contains(r.BotToken, "ABCDEFGH") || strings.Contains(r.APIKeys[0], "supersecret") {
		t.Errorf("Redacted leaked: %+v", r)
	}
	if c.BotToken != "123456:ABCDEFGHWXYZ" {
		t.Error("Redacted must not modify the original")
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:8787": true,
		"localhost:80":   true,
		"[::1]:8787":     true,
		":8787":          false,
		"0.0.0.0:8787":   false,
		"10.0.0.5:8787":  false,
		"example.com:80": false,
		"garbage":        false,
	}
	for addr, want := range cases {
		if got := IsLoopback(addr); got != want {
			t.Errorf("IsLoopback(%q) = %v; want %v", addr, got, want)
		}
	}
}

func TestValidateServer(t *testing.T) {
	good := "dsp_0123456789abcdef"
	tests := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"loopback", Config{APIKeys: []string{good}, Addr: DefaultAddr}, true},
		{"no key", Config{Addr: DefaultAddr}, false},
		{"short key", Config{APIKeys: []string{"short"}, Addr: DefaultAddr}, false},
		{"remote without opt-in", Config{APIKeys: []string{good}, Addr: "0.0.0.0:8787"}, false},
		{"all interfaces without opt-in", Config{APIKeys: []string{good}, Addr: ":8787"}, false},
		{"remote with opt-in", Config{APIKeys: []string{good}, Addr: "0.0.0.0:8787", AllowRemote: true}, true},
		{"half tls", Config{APIKeys: []string{good}, Addr: DefaultAddr, TLSCert: "c.pem"}, false},
		{"bad addr", Config{APIKeys: []string{good}, Addr: "nope"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.ValidateServer()
			if (err == nil) != tt.ok {
				t.Errorf("ValidateServer() = %v; want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestValidateTelegram(t *testing.T) {
	err := (&Config{}).ValidateTelegram()
	if err == nil || !strings.Contains(err.Error(), EnvBotToken) || !strings.Contains(err.Error(), EnvChatID) {
		t.Errorf("got %v", err)
	}
	if err := (&Config{BotToken: "t", ChatID: "c"}).ValidateTelegram(); err != nil {
		t.Error(err)
	}
}
