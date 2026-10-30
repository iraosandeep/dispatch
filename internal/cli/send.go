package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/notification"
)

// maxInputBytes bounds stdin/file reads. Telegram's limit is far smaller; this
// only prevents reading unbounded input before reporting it as too large.
const maxInputBytes = 1 << 20

func (a *App) sendCmd() *cobra.Command {
	var (
		title, priority, emoji, file, chatID string
		useStdin, quiet                      bool
		timeout                              time.Duration
	)

	cmd := &cobra.Command{
		Use:   "send [message]",
		Short: "Send a notification",
		Long: `Send a notification to the configured Telegram chat.

The message comes from the arguments, standard input (--stdin), or a file
(--file). Exactly one source must be used. Messages are sent as plain text and
are never truncated: anything over Telegram's 4096-character limit is rejected.

Priority on Telegram:
  low     delivered silently (no sound)
  normal  default notification behavior
  high    delivered with sound and a 🚨 prefix (cannot override a muted chat)

Exit codes: 0 accepted, 1 delivery failed, 2 invalid input,
3 configuration problem, 4 transient (rate limit/timeout/network).`,
		Example: `  dispatch send "Backup successful"
  dispatch send --title "Import Failed" --priority high "The accounting import failed"
  echo "Deployment succeeded" | dispatch send --stdin
  dispatch send --file ./report.txt --emoji 📊`,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := a.readBody(args, useStdin, file)
			if err != nil {
				return err
			}
			p, err := notification.ParsePriority(priority)
			if err != nil {
				return err
			}
			msg := notification.Message{Title: title, Body: body, Priority: p, Emoji: emoji}
			// Reject bad input before touching configuration or the network.
			if err := msg.Validate(); err != nil {
				return err
			}

			var o config.Overrides
			if cmd.Flags().Changed("chat-id") {
				o.ChatID = &chatID
			}
			if cmd.Flags().Changed("timeout") {
				if timeout <= 0 {
					return usageErrorf("--timeout must be positive")
				}
				o.Timeout = &timeout
			}
			cfg, err := a.loadConfig(o)
			if err != nil {
				return err
			}
			logger := a.cliLogger()
			svc, _, err := a.buildService(cfg, logger)
			if err != nil {
				return err
			}

			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout.Std())
			defer cancel()
			if err := svc.Notify(ctx, msg); err != nil {
				return err
			}
			if !quiet {
				fmt.Fprintln(a.Stderr, "Notification accepted by Telegram.")
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVarP(&title, "title", "t", "", "optional title shown above the message")
	f.StringVarP(&priority, "priority", "p", "normal", "low, normal, or high")
	f.StringVarP(&emoji, "emoji", "e", "", "optional emoji prefix, e.g. ✅")
	f.BoolVar(&useStdin, "stdin", false, "read the message from standard input")
	f.StringVarP(&file, "file", "f", "", "read the message from a file")
	f.StringVar(&chatID, "chat-id", "", "override the destination chat ID")
	f.DurationVar(&timeout, "timeout", config.DefaultTimeout, "total time allowed for delivery, including retries")
	f.BoolVarP(&quiet, "quiet", "q", false, "print nothing on success")
	return cmd
}

// readBody returns the message from exactly one source.
func (a *App) readBody(args []string, useStdin bool, file string) (string, error) {
	sources := 0
	for _, used := range []bool{len(args) > 0, useStdin, file != ""} {
		if used {
			sources++
		}
	}
	switch {
	case sources == 0:
		return "", usageErrorf("no message provided; pass it as an argument, or use --stdin or --file")
	case sources > 1:
		return "", usageErrorf("use only one message source: an argument, --stdin, or --file")
	}

	switch {
	case len(args) > 0:
		// Allow unquoted multi-word messages: dispatch send Backup done
		return strings.Join(args, " "), nil
	case useStdin:
		return readLimited(a.Stdin, "standard input")
	default:
		fh, err := os.Open(file)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", usageErrorf("file not found: %s", file)
			}
			return "", usageErrorf("cannot read file: %v", err)
		}
		defer fh.Close()
		return readLimited(fh, file)
	}
}

func readLimited(r io.Reader, name string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxInputBytes+1))
	if err != nil {
		return "", usageErrorf("read %s: %v", name, err)
	}
	if len(data) > maxInputBytes {
		return "", notification.Errorf(notification.CodeMessageTooLarge,
			"%s exceeds %d bytes; Telegram's limit is %d characters", name, maxInputBytes, notification.MaxTextLength)
	}
	// Drop the trailing newline that echo and most files end with.
	return strings.TrimRight(string(data), "\r\n"), nil
}
