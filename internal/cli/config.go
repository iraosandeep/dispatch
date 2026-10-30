package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/telegram"
)

func (a *App) configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage Dispatch configuration",
	}
	cmd.AddCommand(a.configInitCmd(), a.configShowCmd(), a.configPathCmd(), a.configGenKeyCmd())
	return cmd
}

func (a *App) configShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the effective configuration with secrets masked",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			cfg, err := a.loadConfig(config.Overrides{})
			if err != nil {
				return err
			}
			r := cfg.Redacted()

			fileState := "not found"
			if _, err := os.Stat(cfg.Path); err == nil {
				fileState = "found"
			}
			fmt.Fprintf(a.Stdout, "Config file: %s (%s)\n\n", cfg.Path, fileState)

			apiKeys := "(none)"
			if n := len(r.APIKeys); n > 0 {
				apiKeys = strings.Join(r.APIKeys, ", ")
			}
			tgURL := r.TelegramAPIURL
			if tgURL == "" {
				tgURL = telegram.DefaultBaseURL
			}
			rate := "disabled"
			if r.RateLimit > 0 {
				rate = strconv.FormatFloat(r.RateLimit, 'g', -1, 64) + "/s"
			}

			tw := tabwriter.NewWriter(a.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SETTING\tVALUE\tSOURCE")
			row := func(name, key, value string) {
				if value == "" {
					value = "(not set)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", name, value, cfg.SourceOf(key))
			}
			row("bot_token", "bot_token", r.BotToken)
			row("chat_id", "chat_id", r.ChatID)
			row("api_keys", "api_keys", apiKeys)
			row("addr", "addr", r.Addr)
			row("allow_remote", "allow_remote", strconv.FormatBool(r.AllowRemote))
			row("timeout", "timeout", r.Timeout.Std().String())
			row("rate_limit", "rate_limit", rate)
			row("tls_cert", "tls_cert", r.TLSCert)
			row("tls_key", "tls_key", r.TLSKey)
			row("telegram_api_url", "telegram_api_url", tgURL)
			return tw.Flush()
		},
	}
}

func (a *App) configPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {
			fmt.Fprintln(a.Stdout, config.ResolvePath(a.configPath, a.Getenv))
		},
	}
}

func (a *App) configGenKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gen-key",
		Short: "Generate a random API key for the HTTP server",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			key, err := generateAPIKey()
			if err != nil {
				return err
			}
			fmt.Fprintln(a.Stdout, key)
			return nil
		},
	}
}

func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate API key: %w", err)
	}
	return "dsp_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func (a *App) configInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Interactively configure Telegram credentials",
		Long: `Walk through creating a Telegram bot, finding your chat ID, and saving the
settings to a config file readable only by you.

This command is interactive. For servers, CI, and automation, set
DISPATCH_BOT_TOKEN and DISPATCH_CHAT_ID instead.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if a.IsTerminal == nil || !a.IsTerminal() {
				return configError(errors.New("`config init` is interactive and needs a terminal; " +
					"in automation, set DISPATCH_BOT_TOKEN and DISPATCH_CHAT_ID instead"))
			}
			return a.runInit(cmd.Context())
		},
	}
}

func (a *App) runInit(ctx context.Context) error {
	path := config.ResolvePath(a.configPath, a.Getenv)
	existing, err := config.ReadFile(path)
	if err != nil {
		return configError(err)
	}
	p := &prompter{in: bufio.NewReader(a.Stdin), out: a.Stdout, stdin: a.Stdin}
	out := a.Stdout

	fmt.Fprintf(out, "Dispatch setup — settings will be saved to %s\n\n", path)

	// Step 1: bot token.
	fmt.Fprintln(out, "Step 1: Bot token")
	fmt.Fprintln(out, "  In Telegram, message @BotFather, send /newbot, and copy the token it gives you.")
	token, err := p.secret("  Bot token", existing.BotToken)
	if err != nil {
		return err
	}
	if token == "" {
		return configError(errors.New("a bot token is required"))
	}

	apiURL := existing.TelegramAPIURL
	if v := a.Getenv(config.EnvTelegramAPIURL); v != "" {
		apiURL = v
	}
	client := telegram.New(token, telegram.Options{BaseURL: apiURL})
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	me, err := client.GetMe(callCtx)
	cancel()
	if err != nil {
		return telegram.MapError(err)
	}
	fmt.Fprintf(out, "  ✓ Connected to @%s\n\n", me.Username)

	// Step 2: chat ID.
	fmt.Fprintln(out, "Step 2: Destination chat")
	chatID, err := a.chooseChat(ctx, p, client, me, existing.ChatID)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "  ✓ Using chat %s\n\n", chatID)

	// Step 3: API key for the optional HTTP server.
	fmt.Fprintln(out, "Step 3: HTTP API key (only needed for `dispatch serve`)")
	apiKeys := existing.APIKeys
	if len(apiKeys) > 0 {
		fmt.Fprintf(out, "  Keeping %d existing API key(s).\n\n", len(apiKeys))
	} else if ok, err := p.confirm("  Generate an API key now?", true); err != nil {
		return err
	} else if ok {
		key, err := generateAPIKey()
		if err != nil {
			return err
		}
		apiKeys = []string{key}
		fmt.Fprintf(out, "  API key: %s\n  Clients send it as: Authorization: Bearer <key>\n\n", key)
	} else {
		fmt.Fprintln(out)
	}

	existing.BotToken = token
	existing.ChatID = chatID
	existing.APIKeys = apiKeys
	if err := config.Save(path, existing); err != nil {
		return configError(err)
	}
	fmt.Fprintf(out, "✓ Saved %s (readable only by you)\n\n", path)

	if ok, err := p.confirm("Send a test message now?", true); err != nil {
		return err
	} else if ok {
		cfg, err := a.loadConfig(config.Overrides{})
		if err != nil {
			return err
		}
		testCtx, cancel := context.WithTimeout(ctx, cfg.Timeout.Std())
		defer cancel()
		return a.sendTestMessage(testCtx, cfg)
	}
	fmt.Fprintln(out, "Done. Try: dispatch send \"Hello from Dispatch\"")
	return nil
}

// chooseChat asks for a chat ID, offering to discover it from recent messages
// sent to the bot.
func (a *App) chooseChat(ctx context.Context, p *prompter, client *telegram.Client, me *telegram.User, current string) (string, error) {
	out := a.Stdout
	if current != "" {
		v, err := p.line(fmt.Sprintf("  Chat ID [%s] (Enter to keep, or type \"detect\")", current))
		if err != nil {
			return "", err
		}
		switch v {
		case "":
			return current, nil
		case "detect":
		default:
			return v, nil
		}
	} else {
		v, err := p.line("  Chat ID (leave blank to detect it automatically)")
		if err != nil {
			return "", err
		}
		if v != "" {
			return v, nil
		}
	}

	fmt.Fprintf(out, "  Open Telegram and send any message to @%s.\n", me.Username)
	fmt.Fprintln(out, "  For a group, add the bot to the group and send a message there.")
	for attempt := 0; attempt < 5; attempt++ {
		if _, err := p.line("  Press Enter once you've sent the message"); err != nil {
			return "", err
		}
		callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		updates, err := client.GetUpdates(callCtx)
		cancel()
		if err != nil {
			fmt.Fprintf(out, "  Could not read messages: %s\n", telegram.MapError(err))
			break
		}

		chats := uniqueChats(updates)
		switch len(chats) {
		case 0:
			fmt.Fprintln(out, "  No messages found yet.")
			continue
		case 1:
			c := chats[0]
			ok, err := p.confirm(fmt.Sprintf("  Found %s (%s, id %d). Use it?", c.DisplayName(), c.Type, c.ID), true)
			if err != nil {
				return "", err
			}
			if ok {
				return strconv.FormatInt(c.ID, 10), nil
			}
		default:
			fmt.Fprintln(out, "  Found these chats:")
			for i, c := range chats {
				fmt.Fprintf(out, "    %d) %s (%s, id %d)\n", i+1, c.DisplayName(), c.Type, c.ID)
			}
			v, err := p.line("  Choose a number")
			if err != nil {
				return "", err
			}
			if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= len(chats) {
				return strconv.FormatInt(chats[n-1].ID, 10), nil
			}
			fmt.Fprintln(out, "  Invalid choice.")
		}
	}

	v, err := p.line("  Enter the chat ID manually")
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", configError(errors.New("a chat ID is required"))
	}
	return v, nil
}

func uniqueChats(updates []telegram.Update) []telegram.Chat {
	seen := map[int64]bool{}
	var chats []telegram.Chat
	// Newest first: the chat the user just messaged is most likely wanted.
	for i := len(updates) - 1; i >= 0; i-- {
		if c, ok := updates[i].Chat(); ok && !seen[c.ID] {
			seen[c.ID] = true
			chats = append(chats, c)
		}
	}
	return chats
}

// prompter reads interactive answers.
type prompter struct {
	in    *bufio.Reader
	out   io.Writer
	stdin io.Reader
}

func (p *prompter) line(label string) (string, error) {
	fmt.Fprintf(p.out, "%s: ", label)
	s, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || s == "") {
		return "", usageError(errors.New("setup cancelled"))
	}
	return strings.TrimSpace(s), nil
}

// secret reads a value without echoing it when stdin is a terminal.
func (p *prompter) secret(label, current string) (string, error) {
	if current != "" {
		label += fmt.Sprintf(" [%s] (Enter to keep)", config.Mask(current))
	}
	f, ok := p.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) || p.in.Buffered() > 0 {
		v, err := p.line(label)
		if v == "" {
			v = current
		}
		return v, err
	}
	fmt.Fprintf(p.out, "%s (input hidden): ", label)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(p.out)
	if err != nil {
		return "", usageError(errors.New("setup cancelled"))
	}
	if v := strings.TrimSpace(string(b)); v != "" {
		return v, nil
	}
	return current, nil
}

func (p *prompter) confirm(label string, def bool) (bool, error) {
	hint := "[y/N]"
	if def {
		hint = "[Y/n]"
	}
	v, err := p.line(label + " " + hint)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(v) {
	case "":
		return def, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}
