// Package notification is the provider-agnostic core shared by the CLI and
// the HTTP server: message validation, rendering, and delivery.
package notification

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Priority controls how a notification is presented. Telegram has no native
// priority levels, so priorities are mapped onto what Telegram does support:
//
//   - low:    delivered silently (no sound or vibration on the recipient's device)
//   - normal: delivered with the chat's default notification settings
//   - high:   delivered with sound and prefixed with a 🚨 marker
//
// High priority cannot bypass a muted chat or Do Not Disturb.
type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

const (
	// MaxTextLength is Telegram's sendMessage limit, measured in UTF-16 code
	// units after rendering (title, markers, and body combined).
	MaxTextLength = 4096
	// MaxTitleLength bounds the optional title.
	MaxTitleLength = 256
	// MaxEmojiLength bounds the optional emoji prefix (in runes). Some emoji
	// are several code points (skin tones, ZWJ sequences, flags).
	MaxEmojiLength = 16
	// MaxCaptionLength is Telegram's limit for text sent with an attachment.
	MaxCaptionLength = 1024
	// MaxAttachmentBytes is the largest file a Telegram bot can upload.
	MaxAttachmentBytes = 50 << 20

	highPriorityMarker = "🚨"
)

// ParsePriority converts user input to a Priority. Empty input means normal.
func ParsePriority(s string) (Priority, error) {
	switch p := Priority(strings.ToLower(strings.TrimSpace(s))); p {
	case "":
		return PriorityNormal, nil
	case PriorityLow, PriorityNormal, PriorityHigh:
		return p, nil
	default:
		return "", Errorf(CodeInvalidRequest, "invalid priority %q: use low, normal, or high", s)
	}
}

// Message is a notification as supplied by a caller.
type Message struct {
	Title    string
	Body     string
	Priority Priority
	Emoji    string
	// Attachment is an optional file. When set, Body becomes the caption and
	// may be empty.
	Attachment *Attachment
}

// Attachment is a file delivered with a notification.
type Attachment struct {
	Name string // base file name shown to the recipient
	Data []byte
}

// Rendered is the provider-ready form of a Message.
type Rendered struct {
	// Text is plain text; providers must not interpret it as markup.
	Text string
	// Silent asks the provider to deliver without sound.
	Silent bool
	// Attachment, when non-nil, is sent as a document with Text as caption.
	Attachment *Attachment
}

// Validate checks the message without modifying it. It never truncates.
func (m Message) Validate() error {
	if m.Attachment == nil && strings.TrimSpace(m.Body) == "" {
		return Errorf(CodeEmptyMessage, "message is empty")
	}
	if err := m.Attachment.validate(); err != nil {
		return err
	}
	if !utf8.ValidString(m.Body) || !utf8.ValidString(m.Title) || !utf8.ValidString(m.Emoji) {
		return Errorf(CodeInvalidRequest, "message must be valid UTF-8 text")
	}
	if _, err := ParsePriority(string(m.Priority)); err != nil {
		return err
	}
	if strings.ContainsAny(m.Title, "\r\n") {
		return Errorf(CodeInvalidRequest, "title must be a single line")
	}
	if n := utf8.RuneCountInString(m.Title); n > MaxTitleLength {
		return Errorf(CodeInvalidRequest, "title is %d characters; the maximum is %d", n, MaxTitleLength)
	}
	if strings.ContainsAny(m.Emoji, " \t\r\n") {
		return Errorf(CodeInvalidRequest, "emoji must not contain whitespace")
	}
	if n := utf8.RuneCountInString(m.Emoji); n > MaxEmojiLength {
		return Errorf(CodeInvalidRequest, "emoji is %d characters; the maximum is %d", n, MaxEmojiLength)
	}
	if m.Attachment != nil {
		if n := TextLength(m.Render().Text); n > MaxCaptionLength {
			return Errorf(CodeMessageTooLarge,
				"caption is %d characters after formatting; Telegram's limit with an attachment is %d (Dispatch never truncates messages)",
				n, MaxCaptionLength)
		}
		return nil
	}
	if n := TextLength(m.Render().Text); n > MaxTextLength {
		return Errorf(CodeMessageTooLarge,
			"message is %d characters after formatting; Telegram's limit is %d (Dispatch never truncates messages)",
			n, MaxTextLength)
	}
	return nil
}

func (a *Attachment) validate() error {
	switch {
	case a == nil:
		return nil
	case strings.TrimSpace(a.Name) == "" || strings.ContainsAny(a.Name, "/\\\x00\r\n"):
		return Errorf(CodeInvalidRequest, "attachment needs a plain file name")
	case len(a.Data) == 0:
		return Errorf(CodeInvalidRequest, "attachment %q is empty", a.Name)
	case len(a.Data) > MaxAttachmentBytes:
		return Errorf(CodeAttachmentTooLarge,
			"attachment %q is %s; Telegram bots can upload at most %s", a.Name, FormatBytes(len(a.Data)), FormatBytes(MaxAttachmentBytes))
	}
	return nil
}

// FormatBytes renders a byte count for error messages, e.g. "12.3 MB".
func FormatBytes(n int) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 1, 64) + " KB"
	}
	return strconv.Itoa(n) + " bytes"
}

// Render produces the plain-text form delivered to the provider.
//
//	With a title:      "🚨 🔥 Import Failed\n\nThe accounting import failed"
//	Without a title:   "🚨 🔥 The accounting import failed"
func (m Message) Render() Rendered {
	p, _ := ParsePriority(string(m.Priority))

	var prefix []string
	if p == PriorityHigh {
		prefix = append(prefix, highPriorityMarker)
	}
	if m.Emoji != "" {
		prefix = append(prefix, m.Emoji)
	}

	title := strings.TrimSpace(m.Title)
	header := prefix
	if title != "" {
		header = append(header, title)
	}
	var text string
	switch {
	case len(header) == 0:
		text = m.Body
	case strings.TrimSpace(m.Body) == "": // attachment with only a title/emoji
		text = strings.Join(header, " ")
	case title != "":
		text = strings.Join(header, " ") + "\n\n" + m.Body
	default:
		text = strings.Join(header, " ") + " " + m.Body
	}
	return Rendered{Text: text, Silent: p == PriorityLow, Attachment: m.Attachment}
}

// TextLength counts UTF-16 code units, which is how Telegram measures
// message length. Characters outside the Basic Multilingual Plane (most
// emoji) count as two.
func TextLength(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// String implements fmt.Stringer for log output.
func (p Priority) String() string {
	if p == "" {
		return string(PriorityNormal)
	}
	return string(p)
}
