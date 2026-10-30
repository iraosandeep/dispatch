package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/iraosandeep/dispatch/internal/config"
	"github.com/iraosandeep/dispatch/internal/notification"
	"github.com/iraosandeep/dispatch/internal/telegram"
)

func (a *App) testCmd() *cobra.Command {
	var chatID string
	cmd := &cobra.Command{
		Use:   "test",
		Short: "Verify configuration and send a test message",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var o config.Overrides
			if cmd.Flags().Changed("chat-id") {
				o.ChatID = &chatID
			}
			cfg, err := a.loadConfig(o)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), cfg.Timeout.Std())
			defer cancel()
			return a.sendTestMessage(ctx, cfg)
		},
	}
	cmd.Flags().StringVar(&chatID, "chat-id", "", "override the destination chat ID")
	return cmd
}

func (a *App) sendTestMessage(ctx context.Context, cfg *config.Config) error {
	logger := a.cliLogger()
	svc, client, err := a.buildService(cfg, logger)
	if err != nil {
		return err
	}

	me, err := client.GetMe(ctx)
	if err != nil {
		return telegram.MapError(err)
	}
	fmt.Fprintf(a.Stdout, "Bot:  @%s\nChat: %s\n", me.Username, cfg.ChatID)

	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	err = svc.Notify(ctx, notification.Message{
		Emoji: "✅",
		Title: "Dispatch test",
		Body: fmt.Sprintf("Dispatch is configured correctly.\nHost: %s\nTime: %s",
			host, time.Now().Format(time.RFC1123)),
	})
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Stdout, "Test message accepted by Telegram. Check your Telegram chat.")
	return nil
}
