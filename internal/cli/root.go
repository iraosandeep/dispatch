// Package cli implements the dispatch command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/notification"
	"github.com/iraosandeep/dispatch/internal/telegram"
	"github.com/iraosandeep/dispatch/internal/update"
)

// Exit codes. These are part of the public interface for shell scripts.
const (
	ExitOK        = 0 // notification accepted
	ExitFailure   = 1 // delivery failed or unexpected error
	ExitUsage     = 2 // invalid arguments, message, or attachment (empty, too large)
	ExitConfig    = 3 // missing or rejected configuration (token, chat ID, API key)
	ExitTransient = 4 // rate limit, timeout, or network error; retrying later may succeed
)

// BuildInfo is injected at build time via -ldflags.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// App holds process-level dependencies so commands can be tested in-process.
type App struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	Build  BuildInfo
	// IsTerminal reports whether stdin is an interactive terminal.
	IsTerminal func() bool
	// Updater and Executable support `dispatch update`; injectable for tests.
	Updater    *update.Updater
	Executable func() (string, error)

	configPath string
	verbose    bool
}

// Main runs the CLI against the real process environment and returns the
// exit code.
func Main(build BuildInfo) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app := &App{
		Stdin:      os.Stdin,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
		Getenv:     os.Getenv,
		Build:      build,
		IsTerminal: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) },
		Updater:    &update.Updater{},
		Executable: os.Executable,
	}
	return app.Run(ctx, os.Args[1:])
}

// Run executes the CLI with args and returns the exit code.
func (a *App) Run(ctx context.Context, args []string) int {
	root := a.rootCmd()
	root.SetArgs(args)
	root.SetIn(a.Stdin)
	root.SetOut(a.Stdout)
	root.SetErr(a.Stderr)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintf(a.Stderr, "dispatch: %s\n", err)
	return exitCode(err)
}

func (a *App) rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "dispatch",
		Short: "Send Telegram notifications from any script, app, or server",
		Long: `Dispatch sends notifications to Telegram from the command line or through
an authenticated local HTTP API.

Get started:
  dispatch config init        interactive setup
  dispatch test               send a test message
  dispatch send "Hello"       send a notification
  dispatch serve              start the HTTP API on 127.0.0.1:8787
  dispatch update             upgrade to the latest release`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.PersistentFlags().StringVar(&a.configPath, "config", "", "config file path (default $DISPATCH_CONFIG or ~/.config/dispatch/config.json)")
	root.PersistentFlags().BoolVarP(&a.verbose, "verbose", "v", false, "enable debug logging to stderr")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})

	root.AddCommand(a.sendCmd(), a.testCmd(), a.serveCmd(), a.configCmd(), a.versionCmd(), a.updateCmd())
	return root
}

// loadConfig resolves configuration and prints any warnings.
func (a *App) loadConfig(o config.Overrides) (*config.Config, error) {
	cfg, err := config.Load(config.LoadOptions{Path: a.configPath, Overrides: o, Getenv: a.Getenv})
	if err != nil {
		return nil, configError(err)
	}
	for _, w := range cfg.Warnings {
		fmt.Fprintf(a.Stderr, "dispatch: warning: %s\n", w)
	}
	return cfg, nil
}

func (a *App) newTelegramClient(cfg *config.Config, token string, logger *slog.Logger) *telegram.Client {
	return telegram.New(token, telegram.Options{BaseURL: cfg.TelegramAPIURL, Logger: logger})
}

// buildService constructs the shared notification engine for cfg.
func (a *App) buildService(cfg *config.Config, logger *slog.Logger) (*notification.Service, *telegram.Client, error) {
	if err := cfg.ValidateTelegram(); err != nil {
		return nil, nil, configError(err)
	}
	client := a.newTelegramClient(cfg, cfg.BotToken, logger)
	return notification.NewService(telegram.NewProvider(client, cfg.ChatID), logger), client, nil
}

// cliLogger returns a logger for one-shot commands: quiet unless --verbose.
func (a *App) cliLogger() *slog.Logger {
	level := slog.LevelWarn + 1 // suppress the service's own failure log; Run prints the error
	if a.verbose {
		level = slog.LevelDebug
	}
	return newLogger(a.Stderr, level, "text")
}

// newLogger builds a slog logger that redacts sensitive attributes by key.
func newLogger(w io.Writer, level slog.Level, format string) *slog.Logger {
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			if isSensitiveKey(attr.Key) {
				return slog.String(attr.Key, "<redacted>")
			}
			return attr
		},
	}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}

func isSensitiveKey(key string) bool {
	k := strings.ToLower(key)
	for _, s := range []string{"token", "authorization", "api_key", "apikey", "secret", "password"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// exitError carries an explicit exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func usageError(err error) error  { return &exitError{code: ExitUsage, err: err} }
func configError(err error) error { return &exitError{code: ExitConfig, err: err} }

func usageErrorf(format string, args ...any) error {
	return usageError(fmt.Errorf(format, args...))
}

func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	var ne *notification.Error
	if errors.As(err, &ne) {
		switch ne.Code {
		case notification.CodeInvalidRequest, notification.CodeEmptyMessage, notification.CodeMessageTooLarge,
			notification.CodeAttachmentTooLarge:
			return ExitUsage
		case notification.CodeNotConfigured, notification.CodeInvalidCredentials, notification.CodeInvalidDestination:
			return ExitConfig
		case notification.CodeRateLimited, notification.CodeTimeout, notification.CodeNetwork:
			return ExitTransient
		}
		return ExitFailure
	}
	// Cobra reports unknown commands and argument-count errors as plain errors.
	if msg := err.Error(); strings.HasPrefix(msg, "unknown command") || strings.Contains(msg, "arg(s)") {
		return ExitUsage
	}
	return ExitFailure
}
