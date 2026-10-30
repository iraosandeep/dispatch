// Package telegramtest provides a fake Telegram Bot API for tests.
package telegramtest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is a recorded Bot API call.
type Request struct {
	Method string
	Body   map[string]any
}

// Response is a canned reply. Zero value means {"ok":true,"result":true}.
type Response struct {
	Status int
	Body   string
}

// Server is a fake Bot API that accepts a single bot token.
type Server struct {
	*httptest.Server
	Token string

	mu        sync.Mutex
	requests  []Request
	responses map[string][]Response // queued per method; last one repeats
}

// NewServer starts a fake API that is closed when the test ends.
func NewServer(t testing.TB, token string) *Server {
	s := &Server{Token: token, responses: map[string][]Response{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// Respond queues responses for method. The last response repeats.
func (s *Server) Respond(method string, rs ...Response) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses[method] = rs
}

// Requests returns recorded calls to method (all calls if method is empty).
func (s *Server) Requests(method string) []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.requests {
		if method == "" || r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	token, method, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		return
	}
	if token != s.Token {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
		return
	}

	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)

	s.mu.Lock()
	s.requests = append(s.requests, Request{Method: method, Body: body})
	var resp Response
	if q := s.responses[method]; len(q) > 0 {
		resp = q[0]
		if len(q) > 1 {
			s.responses[method] = q[1:]
		}
	}
	s.mu.Unlock()

	if resp.Status == 0 {
		resp.Status = http.StatusOK
	}
	if resp.Body == "" {
		resp.Body = defaultResult(method)
	}
	w.WriteHeader(resp.Status)
	io.WriteString(w, resp.Body)
}

func defaultResult(method string) string {
	switch method {
	case "getMe":
		return `{"ok":true,"result":{"id":42,"is_bot":true,"first_name":"Dispatch","username":"dispatch_test_bot"}}`
	case "getUpdates":
		return `{"ok":true,"result":[]}`
	default:
		return `{"ok":true,"result":{"message_id":1}}`
	}
}
