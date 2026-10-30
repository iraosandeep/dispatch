package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/notification"
)

// maxInputBytes bounds stdin/file reads. Telegram's limit is far smaller; this
// only prevents reading unbounded input before reporting it as too large.
const maxInputBytes = 1 << 20

// attachmentTimeout replaces the default delivery timeout when uploading a
// file, unless the user set a timeout explicitly.
const attachmentTimeout = 2 * time.Minute

func (a *App) sendCmd() *cobra.Command {
	var (
		title, priority, emoji, file, attach, chatID string
		useStdin, quiet                              bool
		timeout                                      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "send [message]",
		Short: "Send a notification",
		Long: `Send a notification to the configured Telegram chat.

The message comes from the arguments, standard input (--stdin), or a file
(--file). Exactly one source must be used. Messages are sent as plain text and
are never truncated: anything over Telegram's 4096-character limit is rejected.

With --attach, the file is uploaded as a Telegram document (up to 50 MB) and
the message, if any, becomes its caption (up to 1024 characters).

Priority on Telegram:
  low     delivered silently (no sound)
  normal  default notification behavior
  high    delivered with sound and a 🚨 prefix (cannot override a muted chat)

Exit codes: 0 accepted, 1 delivery failed, 2 invalid input,
3 configuration problem, 4 transient (rate limit/timeout/network).`,
		Example: `  dispatch send "Backup successful"
  dispatch send --title "Import Failed" --priority high "The accounting import failed"
  echo "Deployment succeeded" | dispatch send --stdin
  dispatch send --file ./report.txt --emoji 📊
  dispatch send --attach ./backup.log "Nightly backup log"
  dispatch send --attach ./report.pdf --title "Monthly report"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := a.readBody(args, useStdin, file, attach != "")
			if err != nil {
				return err
			}
			var attachment *notification.Attachment
			if attach != "" {
				if attachment, err = readAttachment(attach); err != nil {
					return err
				}
			}
			p, err := notification.ParsePriority(priority)
			if err != nil {
				return err
			}
			msg := notification.Message{Title: title, Body: body, Priority: p, Emoji: emoji, Attachment: attachment}
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

			timeoutDur := cfg.Timeout.Std()
			if attachment != nil && cfg.SourceOf("timeout") == string(config.SourceDefault) {
				timeoutDur = attachmentTimeout
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeoutDur)
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
	f.StringVarP(&file, "file", "f", "", "read the message text from a file")
	f.StringVarP(&attach, "attach", "a", "", "upload a file as a document (max 50 MB); the message becomes its caption")
	f.StringVar(&chatID, "chat-id", "", "override the destination chat ID")
	f.DurationVar(&timeout, "timeout", config.DefaultTimeout, "total time allowed for delivery, including retries")
	f.BoolVarP(&quiet, "quiet", "q", false, "print nothing on success")
	return cmd
}

// readBody returns the message from exactly one source.
// With an attachment, the message is optional.
func (a *App) readBody(args []string, useStdin bool, file string, hasAttachment bool) (string, error) {
	sources := 0
	for _, used := range []bool{len(args) > 0, useStdin, file != ""} {
		if used {
			sources++
		}
	}
	switch {
	case sources == 0 && hasAttachment:
		return "", nil
	case sources == 0:
		return "", usageErrorf("no message provided; pass it as an argument, or use --stdin, --file, or --attach")
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

// readAttachment loads a file for upload, checking size before reading.
func readAttachment(path string) (*notification.Attachment, error) {
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, usageErrorf("attachment not found: %s", path)
	case err != nil:
		return nil, usageErrorf("cannot read attachment: %v", err)
	case info.IsDir():
		return nil, usageErrorf("attachment is a directory: %s (archive it first, e.g. with tar or zip)", path)
	case info.Size() == 0:
		return nil, usageErrorf("attachment is empty: %s", path)
	case info.Size() > notification.MaxAttachmentBytes:
		return nil, notification.Errorf(notification.CodeAttachmentTooLarge,
			"attachment %s is %s; Telegram bots can upload at most %s",
			path, notification.FormatBytes(int(info.Size())), notification.FormatBytes(notification.MaxAttachmentBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, usageErrorf("cannot read attachment: %v", err)
	}
	return &notification.Attachment{Name: filepath.Base(path), Data: data}, nil
}
