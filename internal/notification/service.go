package notification

import (
	"context"
	"log/slog"
	"time"
)

// Provider delivers rendered notifications to a messaging service.
//
// Send returns nil only after the service has accepted the message. Errors
// should be *Error values with user-safe messages.
type Provider interface {
	Name() string
	Send(ctx context.Context, msg Rendered) error
}

// Service validates and delivers notifications. It is safe for concurrent use
// and is shared by the CLI and the HTTP server.
type Service struct {
	provider Provider
	logger   *slog.Logger
}

// NewService returns a Service that delivers through provider.
func NewService(provider Provider, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{provider: provider, logger: logger}
}

// Notify validates m and delivers it. A nil error means the provider accepted
// the message; it does not confirm that the recipient received or read it.
func (s *Service) Notify(ctx context.Context, m Message) error {
	if m.Priority == "" {
		m.Priority = PriorityNormal
	}
	if err := m.Validate(); err != nil {
		return err
	}
	if s == nil || s.provider == nil {
		return Errorf(CodeNotConfigured, "no notification provider is configured")
	}

	start := time.Now()
	err := s.provider.Send(ctx, m.Render())
	attrs := []any{
		slog.String("provider", s.provider.Name()),
		slog.String("priority", m.Priority.String()),
		slog.Int("length", TextLength(m.Body)),
		slog.Duration("duration", time.Since(start)),
	}
	if err != nil {
		s.logger.Warn("notification failed", append(attrs,
			slog.String("code", string(CodeOf(err))),
			slog.String("error", err.Error()))...)
		return err
	}
	s.logger.Info("notification accepted", attrs...)
	return nil
}
