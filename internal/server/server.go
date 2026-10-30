// Package server implements the optional authenticated HTTP API.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/iraosandeep/dispatch/internal/notification"
)

// Limits and timeouts.
const (
	DefaultMaxBodyBytes    = 64 << 10
	DefaultDeliveryTimeout = 30 * time.Second
	DefaultUploadTimeout   = 2 * time.Minute
	// DefaultMaxUploadBytes allows a maximum-size Telegram upload plus form fields.
	DefaultMaxUploadBytes = notification.MaxAttachmentBytes + 1<<20
	shutdownTimeout       = 20 * time.Second
)

// Options configures a Server.
type Options struct {
	Addr    string
	APIKeys []string
	Service *notification.Service
	// Ready reports whether the service has the configuration required to
	// attempt delivery. Nil means always ready.
	Ready           func() error
	Logger          *slog.Logger
	RateLimit       float64 // notify requests per second; 0 disables
	RateBurst       int     // defaults to max(1, ceil(RateLimit*5))
	DeliveryTimeout time.Duration
	// UploadTimeout is the minimum delivery time allowed for requests with
	// an attachment. Default 2m.
	UploadTimeout time.Duration
	// MaxBodyBytes limits JSON bodies and each multipart text field.
	MaxBodyBytes int64
	// MaxUploadBytes limits multipart (file upload) bodies.
	MaxUploadBytes int64
	// MaxConcurrentUploads bounds uploads held in memory at once. Default 4.
	MaxConcurrentUploads int
	TLSCert              string
	TLSKey               string
	Version              string
}

// Server is the Dispatch HTTP API.
type Server struct {
	opts      Options
	logger    *slog.Logger
	handler   http.Handler
	uploadSem chan struct{}
}

// New validates opts and builds a Server.
func New(opts Options) (*Server, error) {
	if len(opts.APIKeys) == 0 {
		return nil, errors.New("at least one API key is required")
	}
	if opts.Service == nil {
		return nil, errors.New("notification service is required")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.DeliveryTimeout <= 0 {
		opts.DeliveryTimeout = DefaultDeliveryTimeout
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	if opts.UploadTimeout <= 0 {
		opts.UploadTimeout = DefaultUploadTimeout
	}
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if opts.MaxConcurrentUploads <= 0 {
		opts.MaxConcurrentUploads = 4
	}
	s := &Server{opts: opts, logger: opts.Logger, uploadSem: make(chan struct{}, opts.MaxConcurrentUploads)}
	s.handler = s.routes()
	return s, nil
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	notify := http.Handler(http.HandlerFunc(s.handleNotify))
	if s.opts.RateLimit > 0 {
		notify = rateLimit(newTokenBucket(s.opts.RateLimit, s.opts.RateBurst), notify)
	}
	notify = requireAPIKey(s.opts.APIKeys, notify)
	notify = allowMethods(notify, http.MethodPost)

	mux.Handle("/notify", notify)
	mux.Handle("/healthz", allowMethods(http.HandlerFunc(s.handleHealth), http.MethodGet, http.MethodHead))
	mux.Handle("/readyz", allowMethods(http.HandlerFunc(s.handleReady), http.MethodGet, http.MethodHead))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, CodeNotFound, "no such endpoint")
	})

	return recoverPanics(s.logger, logRequests(s.logger, securityHeaders(mux)))
}

// ListenAndServe listens on opts.Addr and serves until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.opts.Addr, err)
	}
	return s.Serve(ctx, ln)
}

// Serve accepts connections on ln until ctx is cancelled, then shuts down
// gracefully, allowing in-flight deliveries to finish.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	srv := &http.Server{
		Handler:           s.handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       s.opts.UploadTimeout, // large uploads from slow clients
		WriteTimeout:      max(s.opts.DeliveryTimeout, s.opts.UploadTimeout) + 10*time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelWarn),
	}

	tls := s.opts.TLSCert != ""
	scheme := "http"
	if tls {
		scheme = "https"
	}
	s.logger.Info("dispatch server listening",
		slog.String("addr", ln.Addr().String()),
		slog.String("scheme", scheme),
		slog.String("version", s.opts.Version))

	errCh := make(chan error, 1)
	go func() {
		if tls {
			errCh <- srv.ServeTLS(ln, s.opts.TLSCert, s.opts.TLSKey)
		} else {
			errCh <- srv.Serve(ln)
		}
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	s.logger.Info("shutting down; waiting for in-flight requests")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	s.logger.Info("server stopped")
	return nil
}
