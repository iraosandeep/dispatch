package telegram

import "encoding/json"

// SendMessageRequest is the body of a sendMessage call. ParseMode is never
// set, so Telegram treats Text as plain text and no escaping is required.
type SendMessageRequest struct {
	ChatID              string              `json:"chat_id"`
	Text                string              `json:"text"`
	DisableNotification bool                `json:"disable_notification,omitempty"`
	LinkPreviewOptions  *LinkPreviewOptions `json:"link_preview_options,omitempty"`
}

// LinkPreviewOptions controls URL previews in a message.
type LinkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

// Chat is a Telegram private chat, group, supergroup, or channel.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

// DisplayName returns a human-readable label for the chat.
func (c Chat) DisplayName() string {
	switch {
	case c.Title != "":
		return c.Title
	case c.FirstName != "" || c.LastName != "":
		name := c.FirstName
		if c.LastName != "" {
			if name != "" {
				name += " "
			}
			name += c.LastName
		}
		return name
	case c.Username != "":
		return "@" + c.Username
	}
	return "unknown"
}

// Update is the subset of a Telegram update needed to discover chat IDs.
type Update struct {
	UpdateID     int64          `json:"update_id"`
	Message      *chatContainer `json:"message,omitempty"`
	ChannelPost  *chatContainer `json:"channel_post,omitempty"`
	MyChatMember *chatContainer `json:"my_chat_member,omitempty"`
}

type chatContainer struct {
	Chat Chat `json:"chat"`
}

// Chat returns the chat the update belongs to, if any.
func (u Update) Chat() (Chat, bool) {
	for _, c := range []*chatContainer{u.Message, u.ChannelPost, u.MyChatMember} {
		if c != nil {
			return c.Chat, true
		}
	}
	return Chat{}, false
}

type apiResponse struct {
	OK          bool                `json:"ok"`
	Result      json.RawMessage     `json:"result,omitempty"`
	ErrorCode   int                 `json:"error_code,omitempty"`
	Description string              `json:"description,omitempty"`
	Parameters  *responseParameters `json:"parameters,omitempty"`
}

type responseParameters struct {
	RetryAfter      int   `json:"retry_after,omitempty"`
	MigrateToChatID int64 `json:"migrate_to_chat_id,omitempty"`
}
