// Package config loads Dispatch settings from flags, environment variables,
// and an optional JSON file, in that order of precedence.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvConfig         = "DISPATCH_CONFIG"
	EnvBotToken       = "DISPATCH_BOT_TOKEN"
	EnvChatID         = "DISPATCH_CHAT_ID"
	EnvAPIKey         = "DISPATCH_API_KEY"
	EnvAddr           = "DISPATCH_ADDR"
	EnvAllowRemote    = "DISPATCH_ALLOW_REMOTE"
	EnvTimeout        = "DISPATCH_TIMEOUT"
	EnvRateLimit      = "DISPATCH_RATE_LIMIT"
	EnvTLSCert        = "DISPATCH_TLS_CERT"
	EnvTLSKey         = "DISPATCH_TLS_KEY"
	EnvTelegramAPIURL = "DISPATCH_TELEGRAM_API_URL"
)

// Defaults.
const (
	DefaultAddr      = "127.0.0.1:8787"
	DefaultTimeout   = 30 * time.Second
	DefaultRateLimit = 1.0 // requests per second; Telegram allows ~1 msg/s per chat
	MinAPIKeyLength  = 16
)

// Source identifies where a setting came from.
type Source string

const (
	SourceDefault Source = "default"
	SourceFile    Source = "file"
	SourceEnv     Source = "env"
	SourceFlag    Source = "flag"
)

// Config holds every Dispatch setting.
type Config struct {
	BotToken       string   `json:"bot_token,omitempty"`
	ChatID         string   `json:"chat_id,omitempty"`
	APIKeys        []string `json:"api_keys,omitempty"`
	Addr           string   `json:"addr,omitempty"`
	AllowRemote    bool     `json:"allow_remote,omitempty"`
	Timeout        Duration `json:"timeout,omitempty"`
	RateLimit      float64  `json:"rate_limit,omitempty"`
	TLSCert        string   `json:"tls_cert,omitempty"`
	TLSKey         string   `json:"tls_key,omitempty"`
	TelegramAPIURL string   `json:"telegram_api_url,omitempty"`

	// Populated by Load; not persisted.
	Path     string            `json:"-"`
	Sources  map[string]Source `json:"-"`
	Warnings []string          `json:"-"`
}

// Overrides are explicit command-line values. Nil means "not set".
// The bot token and API keys deliberately have no flags: command-line
// arguments are visible to other users via process listings.
type Overrides struct {
	ChatID      *string
	Addr        *string
	AllowRemote *bool
	Timeout     *time.Duration
	RateLimit   *float64
	TLSCert     *string
	TLSKey      *string
}

// LoadOptions controls Load.
type LoadOptions struct {
	// Path is an explicit config file path. If empty, DISPATCH_CONFIG or the
	// default location is used. An explicit path must exist.
	Path      string
	Overrides Overrides
	// Getenv defaults to os.Getenv; injectable for tests.
	Getenv func(string) string
}

// Load resolves configuration with precedence flags > env > file > defaults.
func Load(opts LoadOptions) (*Config, error) {
	getenv := opts.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}

	cfg := &Config{
		Addr:      DefaultAddr,
		Timeout:   Duration(DefaultTimeout),
		RateLimit: DefaultRateLimit,
		Sources:   map[string]Source{},
	}
	for _, k := range []string{"addr", "timeout", "rate_limit"} {
		cfg.Sources[k] = SourceDefault
	}

	// 1. File.
	path, explicit := opts.Path, opts.Path != ""
	if !explicit {
		if p := getenv(EnvConfig); p != "" {
			path, explicit = p, true
		} else {
			path = DefaultPath(getenv)
		}
	}
	cfg.Path = path
	if err := cfg.applyFile(path, explicit); err != nil {
		return nil, err
	}

	// 2. Environment.
	if err := cfg.applyEnv(getenv); err != nil {
		return nil, err
	}

	// 3. Flags.
	cfg.applyOverrides(opts.Overrides)

	return cfg, nil
}

func (c *Config) applyFile(path string, explicit bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return nil
		}
		return fmt.Errorf("read config file: %w", err)
	}

	var f Config
	if err := json.Unmarshal(data, &f); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}

	if runtime.GOOS != "windows" && (f.BotToken != "" || len(f.APIKeys) > 0) {
		if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
			c.Warnings = append(c.Warnings, fmt.Sprintf(
				"config file %s contains secrets but is accessible by other users (mode %04o); run: chmod 600 %q",
				path, info.Mode().Perm(), path))
		}
	}

	set := func(key string, ok bool, apply func()) {
		if ok {
			apply()
			c.Sources[key] = SourceFile
		}
	}
	set("bot_token", f.BotToken != "", func() { c.BotToken = f.BotToken })
	set("chat_id", f.ChatID != "", func() { c.ChatID = f.ChatID })
	set("api_keys", len(f.APIKeys) > 0, func() { c.APIKeys = cleanKeys(f.APIKeys) })
	set("addr", f.Addr != "", func() { c.Addr = f.Addr })
	set("allow_remote", f.AllowRemote, func() { c.AllowRemote = true })
	set("timeout", f.Timeout > 0, func() { c.Timeout = f.Timeout })
	set("rate_limit", f.RateLimit != 0, func() { c.RateLimit = f.RateLimit })
	set("tls_cert", f.TLSCert != "", func() { c.TLSCert = f.TLSCert })
	set("tls_key", f.TLSKey != "", func() { c.TLSKey = f.TLSKey })
	set("telegram_api_url", f.TelegramAPIURL != "", func() { c.TelegramAPIURL = f.TelegramAPIURL })
	return nil
}

func (c *Config) applyEnv(getenv func(string) string) error {
	str := func(env, key string, dst *string) {
		if v := strings.TrimSpace(getenv(env)); v != "" {
			*dst = v
			c.Sources[key] = SourceEnv
		}
	}
	str(EnvBotToken, "bot_token", &c.BotToken)
	str(EnvChatID, "chat_id", &c.ChatID)
	str(EnvAddr, "addr", &c.Addr)
	str(EnvTLSCert, "tls_cert", &c.TLSCert)
	str(EnvTLSKey, "tls_key", &c.TLSKey)
	str(EnvTelegramAPIURL, "telegram_api_url", &c.TelegramAPIURL)

	// Comma-separated to support key rotation: add the new key, migrate
	// clients, then remove the old key.
	if v := getenv(EnvAPIKey); strings.TrimSpace(v) != "" {
		c.APIKeys = cleanKeys(strings.Split(v, ","))
		c.Sources["api_keys"] = SourceEnv
	}
	if v := strings.TrimSpace(getenv(EnvAllowRemote)); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s: invalid boolean %q", EnvAllowRemote, v)
		}
		c.AllowRemote = b
		c.Sources["allow_remote"] = SourceEnv
	}
	if v := strings.TrimSpace(getenv(EnvTimeout)); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return fmt.Errorf("%s: invalid duration %q (example: 30s)", EnvTimeout, v)
		}
		c.Timeout = Duration(d)
		c.Sources["timeout"] = SourceEnv
	}
	if v := strings.TrimSpace(getenv(EnvRateLimit)); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("%s: invalid number %q", EnvRateLimit, v)
		}
		c.RateLimit = f
		c.Sources["rate_limit"] = SourceEnv
	}
	return nil
}

func (c *Config) applyOverrides(o Overrides) {
	if o.ChatID != nil {
		c.ChatID, c.Sources["chat_id"] = *o.ChatID, SourceFlag
	}
	if o.Addr != nil {
		c.Addr, c.Sources["addr"] = *o.Addr, SourceFlag
	}
	if o.AllowRemote != nil {
		c.AllowRemote, c.Sources["allow_remote"] = *o.AllowRemote, SourceFlag
	}
	if o.Timeout != nil {
		c.Timeout, c.Sources["timeout"] = Duration(*o.Timeout), SourceFlag
	}
	if o.RateLimit != nil {
		c.RateLimit, c.Sources["rate_limit"] = *o.RateLimit, SourceFlag
	}
	if o.TLSCert != nil {
		c.TLSCert, c.Sources["tls_cert"] = *o.TLSCert, SourceFlag
	}
	if o.TLSKey != nil {
		c.TLSKey, c.Sources["tls_key"] = *o.TLSKey, SourceFlag
	}
}

// ValidateTelegram reports missing Telegram settings.
func (c *Config) ValidateTelegram() error {
	var missing []string
	if c.BotToken == "" {
		missing = append(missing, EnvBotToken)
	}
	if c.ChatID == "" {
		missing = append(missing, EnvChatID)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing %s; run `dispatch config init` or set the environment variable(s)",
			strings.Join(missing, " and "))
	}
	return nil
}

// ValidateServer checks settings required by `dispatch serve`.
func (c *Config) ValidateServer() error {
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("no API key configured; set %s or run `dispatch config init` (the HTTP API never runs unauthenticated)", EnvAPIKey)
	}
	for _, k := range c.APIKeys {
		if len(k) < MinAPIKeyLength {
			return fmt.Errorf("API keys must be at least %d characters; generate one with `dispatch config gen-key`", MinAPIKeyLength)
		}
	}
	if _, _, err := net.SplitHostPort(c.Addr); err != nil {
		return fmt.Errorf("invalid listen address %q: %v", c.Addr, err)
	}
	if !IsLoopback(c.Addr) && !c.AllowRemote {
		return fmt.Errorf("refusing to listen on non-loopback address %q without --allow-remote; "+
			"expose Dispatch only on a private network, VPN, or behind an authenticated TLS proxy", c.Addr)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("both --tls-cert and --tls-key are required to enable TLS")
	}
	if c.RateLimit < 0 {
		return errors.New("rate limit must be zero (disabled) or positive")
	}
	return nil
}

// IsLoopback reports whether addr (host:port) binds only to the loopback
// interface. An empty host binds every interface and is not loopback.
func IsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// DefaultPath returns the default config file location:
// $XDG_CONFIG_HOME/dispatch/config.json, ~/.config/dispatch/config.json,
// or %AppData%\dispatch\config.json on Windows.
func DefaultPath(getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "dispatch", "config.json")
	}
	if runtime.GOOS == "windows" {
		if dir, err := os.UserConfigDir(); err == nil {
			return filepath.Join(dir, "dispatch", "config.json")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "dispatch", "config.json")
	}
	return "dispatch.json"
}

// ReadFile returns only the settings stored in the file at path, ignoring
// environment variables. A missing file yields an empty Config.
func ReadFile(path string) (*Config, error) {
	c := &Config{Path: path, Sources: map[string]Source{}}
	if err := c.applyFile(path, false); err != nil {
		return nil, err
	}
	return c, nil
}

// ResolvePath returns the config file path Load would use.
func ResolvePath(explicit string, getenv func(string) string) string {
	if getenv == nil {
		getenv = os.Getenv
	}
	if explicit != "" {
		return explicit
	}
	if p := getenv(EnvConfig); p != "" {
		return p
	}
	return DefaultPath(getenv)
}

// Save writes the persistable settings to path, readable only by the owner.
func Save(path string, c *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return fmt.Errorf("set config permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Mask hides a secret, revealing at most its last four characters.
func Mask(s string) string {
	switch {
	case s == "":
		return ""
	case len(s) < 12:
		return "********"
	default:
		return "********" + s[len(s)-4:]
	}
}

// Redacted returns a copy with all secrets masked, suitable for display.
func (c *Config) Redacted() Config {
	r := *c
	r.BotToken = Mask(c.BotToken)
	r.APIKeys = make([]string, len(c.APIKeys))
	for i, k := range c.APIKeys {
		r.APIKeys[i] = Mask(k)
	}
	return r
}

// SourceOf returns where key was set, or "unset".
func (c *Config) SourceOf(key string) string {
	if s, ok := c.Sources[key]; ok {
		return string(s)
	}
	return "unset"
}

func cleanKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k = strings.TrimSpace(k); k != "" {
			out = append(out, k)
		}
	}
	return out
}

// Duration is a time.Duration that encodes as a string like "30s" in JSON.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string like \"30s\": %w", err)
	}
	v, err := time.ParseDuration(s)
	if err != nil || v < 0 {
		return fmt.Errorf("invalid duration %q", s)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }
