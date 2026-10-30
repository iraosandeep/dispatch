package server

import (
	"log/slog"
	"math"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/iraosandeep/dispatch/internal/notification"
)

// allowMethods rejects requests whose method is not listed.
func allowMethods(next http.Handler, methods ...string) http.Handler {
	allow := strings.Join(methods, ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, m := range methods {
			if r.Method == m {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Allow", allow)
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "method "+r.Method+" not allowed; use "+allow)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// logRequests logs one line per request. It never logs headers (which carry
// the API key) or bodies (which carry message content).
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" {
			level = slog.LevelDebug
		}
		logger.Log(r.Context(), level, "http request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.status),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote", r.RemoteAddr))
	})
}

func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger.Error("panic serving request",
					slog.Any("panic", v),
					slog.String("stack", string(debug.Stack())))
				writeError(w, http.StatusInternalServerError, CodeInternal, "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// rateLimit rejects requests beyond the bucket's rate with 429.
func rateLimit(b *tokenBucket, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if wait, ok := b.take(); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
			writeError(w, http.StatusTooManyRequests, notification.CodeRateLimited, "too many requests; slow down")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenBucket is a minimal thread-safe token-bucket limiter.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64 // tokens per second
	burst  float64
	tokens float64
	last   time.Time
	now    func() time.Time
}

func newTokenBucket(rate float64, burst int) *tokenBucket {
	if burst <= 0 {
		burst = max(1, int(math.Ceil(rate*5)))
	}
	return &tokenBucket{rate: rate, burst: float64(burst), tokens: float64(burst), now: time.Now, last: time.Now()}
}

// take consumes a token, or returns how long until one is available.
func (b *tokenBucket) take() (time.Duration, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.tokens = math.Min(b.burst, b.tokens+now.Sub(b.last).Seconds()*b.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return 0, true
	}
	return time.Duration((1 - b.tokens) / b.rate * float64(time.Second)), false
}
