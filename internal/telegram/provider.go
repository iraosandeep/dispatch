package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/iraosandeep/dispatch/internal/notification"
)

// Provider adapts Client to notification.Provider for a single chat.
type Provider struct {
	client *Client
	chatID string
}

// NewProvider returns a provider that sends to chatID.
func NewProvider(client *Client, chatID string) *Provider {
	return &Provider{client: client, chatID: chatID}
}

func (p *Provider) Name() string { return "telegram" }

// Send delivers msg. A nil error means Telegram accepted the message.
func (p *Provider) Send(ctx context.Context, msg notification.Rendered) error {
	err := p.client.SendMessage(ctx, SendMessageRequest{
		ChatID:              p.chatID,
		Text:                msg.Text,
		DisableNotification: msg.Silent,
	})
	return MapError(err)
}

// MapError converts Telegram client errors into categorized notification
// errors with actionable messages.
func MapError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		desc := strings.ToLower(apiErr.Description)
		switch {
		case apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusNotFound:
			return wrap(notification.CodeInvalidCredentials, err,
				"Telegram rejected the bot token; check DISPATCH_BOT_TOKEN")
		case apiErr.MigrateToChatID != 0:
			return wrap(notification.CodeInvalidDestination, err,
				fmt.Sprintf("the group was upgraded to a supergroup; set DISPATCH_CHAT_ID to %d", apiErr.MigrateToChatID))
		case apiErr.StatusCode == http.StatusBadRequest && strings.Contains(desc, "chat not found"):
			return wrap(notification.CodeInvalidDestination, err,
				"Telegram chat not found; check DISPATCH_CHAT_ID and make sure the chat has started a conversation with the bot")
		case apiErr.StatusCode == http.StatusBadRequest && strings.Contains(desc, "message is too long"):
			return wrap(notification.CodeMessageTooLarge, err, "Telegram rejected the message as too long")
		case apiErr.StatusCode == http.StatusForbidden:
			return wrap(notification.CodeInvalidDestination, err,
				"the bot cannot message this chat ("+apiErr.Description+"); unblock the bot or add it to the group")
		case apiErr.StatusCode == http.StatusConflict:
			return wrap(notification.CodeDeliveryFailed, err,
				"Telegram reported a conflict ("+apiErr.Description+")")
		case apiErr.StatusCode == http.StatusTooManyRequests:
			e := wrap(notification.CodeRateLimited, err, "Telegram rate limit exceeded")
			if apiErr.RetryAfter > 0 {
				e.RetryAfter = apiErr.RetryAfter
				e.Message = fmt.Sprintf("Telegram rate limit exceeded; retry after %s", apiErr.RetryAfter)
			}
			return e
		case apiErr.StatusCode >= 500:
			return wrap(notification.CodeDeliveryFailed, err,
				fmt.Sprintf("Telegram API is unavailable (HTTP %d)", apiErr.StatusCode))
		default:
			return wrap(notification.CodeDeliveryFailed, err,
				"Telegram could not accept the notification: "+apiErr.Description)
		}
	}

	var netErr *NetworkError
	if errors.As(err, &netErr) {
		switch {
		case netErr.DNS:
			return wrap(notification.CodeNetwork, err,
				"could not resolve the Telegram API host; check DNS and network connectivity")
		case netErr.Timeout:
			return wrap(notification.CodeTimeout, err,
				"timed out contacting Telegram; the message may or may not have been delivered")
		default:
			return wrap(notification.CodeNetwork, err,
				"network error contacting Telegram: "+netErr.Error())
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return wrap(notification.CodeTimeout, err, "timed out contacting Telegram")
	}
	return wrap(notification.CodeDeliveryFailed, err, err.Error())
}

func wrap(code notification.Code, err error, msg string) *notification.Error {
	return &notification.Error{Code: code, Message: msg, Err: err}
}
