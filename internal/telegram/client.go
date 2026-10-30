// Package telegram is a minimal Telegram Bot API client built on net/http.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the public Telegram Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

const maxResponseBytes = 1 << 20

// Options configures a Client. Zero values select safe defaults.
type Options struct {
	BaseURL    string
	HTTPClient *http.Client
	// MaxAttempts is the total number of attempts per call, including the
	// first. Default 3.
	MaxAttempts int
	// BaseBackoff is the delay before the first retry; it doubles on each
	// subsequent retry up to MaxBackoff. Defaults 500ms and 8s.
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	// MaxRetryAfter is the longest Telegram-requested rate-limit wait Dispatch
	// will honor automatically. Longer waits fail with a rate-limit error.
	// Default 30s.
	MaxRetryAfter time.Duration
	// AttemptTimeout bounds each JSON API attempt so a hung connection can be
	// retried. Uploads are bounded only by the caller's context. Default 15s.
	AttemptTimeout time.Duration
	Logger         *slog.Logger
}

// Client calls the Telegram Bot API. It is safe for concurrent use.
type Client struct {
	token         string
	baseURL       string
	http          *http.Client
	maxAttempts   int
	baseBackoff   time.Duration
	maxBackoff    time.Duration
	maxRetryAfter time.Duration
	attemptTO     time.Duration
	logger        *slog.Logger
	sleep         func(context.Context, time.Duration) error
}

// New returns a Client authenticated with the given bot token.
func New(token string, opts Options) *Client {
	c := &Client{
		token:         token,
		baseURL:       strings.TrimRight(opts.BaseURL, "/"),
		http:          opts.HTTPClient,
		maxAttempts:   opts.MaxAttempts,
		baseBackoff:   opts.BaseBackoff,
		maxBackoff:    opts.MaxBackoff,
		maxRetryAfter: opts.MaxRetryAfter,
		attemptTO:     opts.AttemptTimeout,
		logger:        opts.Logger,
		sleep:         sleepContext,
	}
	if c.baseURL == "" {
		c.baseURL = DefaultBaseURL
	}
	if c.http == nil {
		// No client-wide timeout: uploads can take minutes. Every call is
		// bounded by the caller's context and, for JSON calls, attemptTO.
		c.http = &http.Client{}
	}
	if c.attemptTO <= 0 {
		c.attemptTO = 15 * time.Second
	}
	if c.maxAttempts <= 0 {
		c.maxAttempts = 3
	}
	if c.baseBackoff <= 0 {
		c.baseBackoff = 500 * time.Millisecond
	}
	if c.maxBackoff <= 0 {
		c.maxBackoff = 8 * time.Second
	}
	if c.maxRetryAfter <= 0 {
		c.maxRetryAfter = 30 * time.Second
	}
	if c.logger == nil {
		c.logger = slog.New(slog.DiscardHandler)
	}
	return c
}

// APIError is an error response from the Telegram Bot API.
type APIError struct {
	Method          string
	StatusCode      int
	Description     string
	RetryAfter      time.Duration
	MigrateToChatID int64
}

func (e *APIError) Error() string {
	desc := e.Description
	if desc == "" {
		desc = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("telegram %s: HTTP %d: %s", e.Method, e.StatusCode, desc)
}

// Temporary reports whether the request may succeed if retried.
func (e *APIError) Temporary() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// NetworkError is a transport-level failure. Its message never contains the
// bot token. A NetworkError after the request was written is ambiguous:
// Telegram may or may not have accepted the message.
type NetworkError struct {
	Method  string
	Timeout bool
	DNS     bool
	msg     string
}

func (e *NetworkError) Error() string { return e.msg }

// GetMe validates the token and returns the bot's identity.
func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := c.call(ctx, "getMe", struct{}{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUpdates returns pending updates. It is used during setup to discover the
// chat ID of a user or group that has messaged the bot.
func (c *Client) GetUpdates(ctx context.Context) ([]Update, error) {
	var updates []Update
	req := map[string]any{"limit": 100, "timeout": 0}
	if err := c.call(ctx, "getUpdates", req, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// SendMessage sends a plain-text message.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) error {
	return c.call(ctx, "sendMessage", req, nil)
}

// SendDocument uploads a file with an optional plain-text caption.
func (c *Client) SendDocument(ctx context.Context, req SendDocumentRequest) error {
	var head bytes.Buffer
	mw := multipart.NewWriter(&head)
	fields := [][2]string{{"chat_id", req.ChatID}}
	if req.Caption != "" {
		fields = append(fields, [2]string{"caption", req.Caption})
	}
	if req.DisableNotification {
		fields = append(fields, [2]string{"disable_notification", "true"})
	}
	for _, f := range fields {
		if err := mw.WriteField(f[0], f[1]); err != nil {
			return fmt.Errorf("telegram sendDocument: encode request: %w", err)
		}
	}
	if _, err := mw.CreateFormFile("document", req.FileName); err != nil {
		return fmt.Errorf("telegram sendDocument: encode request: %w", err)
	}
	// Stream the file between the multipart header and the closing boundary
	// (what mw.Close would write) instead of copying it into the buffer.
	tail := "\r\n--" + mw.Boundary() + "--\r\n"
	contentType := mw.FormDataContentType()
	size := int64(head.Len() + len(req.Data) + len(tail))

	body := func() (io.Reader, string, int64) {
		return io.MultiReader(bytes.NewReader(head.Bytes()), bytes.NewReader(req.Data), strings.NewReader(tail)),
			contentType, size
	}
	return c.retry(ctx, "sendDocument", body, nil, 0)
}

// requestBody produces a fresh request body for each attempt.
type requestBody func() (r io.Reader, contentType string, size int64)

// call performs a JSON Bot API request with bounded retries.
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("telegram %s: encode request: %w", method, err)
	}
	body := func() (io.Reader, string, int64) {
		return bytes.NewReader(data), "application/json", int64(len(data))
	}
	return c.retry(ctx, method, body, out, c.attemptTO)
}

// retry runs a request, retrying transient failures with backoff. A positive
// attemptTimeout bounds each individual attempt.
func (c *Client) retry(ctx context.Context, method string, body requestBody, out any, attemptTimeout time.Duration) error {
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel := ctx, context.CancelFunc(func() {})
		if attemptTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(ctx, attemptTimeout)
		}
		err := c.do(attemptCtx, method, body, out)
		cancel()
		if err == nil {
			return nil
		}
		if attempt >= c.maxAttempts || ctx.Err() != nil {
			return err
		}
		delay, ok := c.retryDelay(err, attempt)
		if !ok {
			return err
		}
		if deadline, has := ctx.Deadline(); has && time.Until(deadline) < delay {
			// Waiting would outlive the caller's deadline; report the real cause.
			return err
		}
		c.logger.Debug("retrying telegram request",
			slog.String("method", method),
			slog.Int("attempt", attempt),
			slog.Duration("delay", delay),
			slog.String("error", err.Error()))
		if c.sleep(ctx, delay) != nil {
			return err
		}
	}
}

func (c *Client) retryDelay(err error, attempt int) (time.Duration, bool) {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if !apiErr.Temporary() {
			return 0, false
		}
		if apiErr.RetryAfter > 0 {
			if apiErr.RetryAfter > c.maxRetryAfter {
				return 0, false
			}
			return apiErr.RetryAfter, true
		}
		return c.backoff(attempt), true
	}
	var netErr *NetworkError
	if errors.As(err, &netErr) {
		return c.backoff(attempt), true
	}
	return 0, false
}

// backoff returns an exponential delay with up to 20% jitter.
func (c *Client) backoff(attempt int) time.Duration {
	d := c.baseBackoff << (attempt - 1)
	if d <= 0 || d > c.maxBackoff {
		d = c.maxBackoff
	}
	return d + time.Duration(rand.Int64N(int64(d)/5+1))
}

func (c *Client) do(ctx context.Context, method string, body requestBody, out any) error {
	endpoint := c.baseURL + "/bot" + c.token + "/" + method
	reader, contentType, size := body()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, reader)
	if err != nil {
		return fmt.Errorf("telegram %s: %s", method, c.redact(err.Error()))
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return c.networkError(method, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return c.networkError(method, err)
	}

	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return &APIError{
			Method:      method,
			StatusCode:  resp.StatusCode,
			Description: "unexpected non-JSON response from Telegram API",
		}
	}
	if !r.OK {
		e := &APIError{Method: method, StatusCode: resp.StatusCode, Description: c.redact(r.Description)}
		if r.ErrorCode != 0 {
			e.StatusCode = r.ErrorCode
		}
		if r.Parameters != nil {
			e.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
			e.MigrateToChatID = r.Parameters.MigrateToChatID
		}
		return e
	}
	if out != nil && len(r.Result) > 0 {
		if err := json.Unmarshal(r.Result, out); err != nil {
			return fmt.Errorf("telegram %s: decode result: %w", method, err)
		}
	}
	return nil
}

func (c *Client) networkError(method string, err error) *NetworkError {
	e := &NetworkError{Method: method}
	var dnsErr *net.DNSError
	var nErr net.Error
	switch {
	case errors.As(err, &dnsErr):
		e.DNS = true
		e.Timeout = dnsErr.IsTimeout
		e.msg = fmt.Sprintf("telegram %s: DNS lookup failed for %s", method, dnsErr.Name)
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &nErr) && nErr.Timeout():
		e.Timeout = true
		e.msg = fmt.Sprintf("telegram %s: request timed out", method)
	case errors.Is(err, context.Canceled):
		e.msg = fmt.Sprintf("telegram %s: request cancelled", method)
	default:
		// *url.Error embeds the request URL, which contains the bot token.
		// Report only the underlying cause, and redact as a second line of
		// defense.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		e.msg = fmt.Sprintf("telegram %s: %s", method, c.redact(err.Error()))
	}
	return e
}

func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "<redacted>")
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
