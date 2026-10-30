package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"strconv"

	"github.com/iraosandeep/dispatch/internal/notification"
)

// HTTP-only error codes. Delivery and validation codes come from the
// notification package.
const (
	CodeUnauthorized         notification.Code = "UNAUTHORIZED"
	CodeMethodNotAllowed     notification.Code = "METHOD_NOT_ALLOWED"
	CodeNotFound             notification.Code = "NOT_FOUND"
	CodePayloadTooLarge      notification.Code = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMediaType notification.Code = "UNSUPPORTED_MEDIA_TYPE"
	CodeNotReady             notification.Code = "NOT_READY"
	CodeInternal             notification.Code = "INTERNAL_ERROR"
)

type notifyRequest struct {
	Message  string `json:"message"`
	Title    string `json:"title,omitempty"`
	Priority string `json:"priority,omitempty"`
	Emoji    string `json:"emoji,omitempty"`
}

type successResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type errorResponse struct {
	OK    bool      `json:"ok"`
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    notification.Code `json:"code"`
	Message string            `json:"message"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType, "Content-Type must be application/json")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req notifyRequest
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				"request body exceeds "+strconv.FormatInt(s.opts.MaxBodyBytes, 10)+" bytes")
			return
		}
		writeError(w, http.StatusBadRequest, notification.CodeInvalidRequest, "invalid JSON body: "+jsonErrorMessage(err))
		return
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, notification.CodeInvalidRequest, "request body must contain a single JSON object")
		return
	}

	priority, err := notification.ParsePriority(req.Priority)
	if err != nil {
		writeNotificationError(w, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), s.opts.DeliveryTimeout)
	defer cancel()

	err = s.opts.Service.Notify(ctx, notification.Message{
		Title:    req.Title,
		Body:     req.Message,
		Priority: priority,
		Emoji:    req.Emoji,
	})
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{OK: true, Message: "Notification accepted"})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "alive"})
}

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	if s.opts.Ready != nil {
		if err := s.opts.Ready(); err != nil {
			writeError(w, http.StatusServiceUnavailable, CodeNotReady, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "ready"})
}

// statusFor maps notification error codes to HTTP status codes.
func statusFor(code notification.Code) int {
	switch code {
	case notification.CodeInvalidRequest, notification.CodeEmptyMessage:
		return http.StatusBadRequest
	case notification.CodeMessageTooLarge:
		return http.StatusRequestEntityTooLarge
	case notification.CodeRateLimited:
		return http.StatusTooManyRequests
	case notification.CodeNotConfigured:
		return http.StatusServiceUnavailable
	case notification.CodeTimeout:
		return http.StatusGatewayTimeout
	default: // credentials, destination, network, delivery: upstream failures
		return http.StatusBadGateway
	}
}

func writeNotificationError(w http.ResponseWriter, err error) {
	var nerr *notification.Error
	if !errors.As(err, &nerr) {
		writeError(w, http.StatusBadGateway, notification.CodeDeliveryFailed, "Telegram could not accept the notification")
		return
	}
	if nerr.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(nerr.RetryAfter.Seconds()))))
	}
	writeError(w, statusFor(nerr.Code), nerr.Code, nerr.Message)
}

func writeError(w http.ResponseWriter, status int, code notification.Code, msg string) {
	writeJSON(w, status, errorResponse{Error: errorBody{Code: code, Message: msg}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonErrorMessage describes a decode error without echoing request content.
func jsonErrorMessage(err error) string {
	var syntaxErr *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "body is empty"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "body is truncated"
	case errors.As(err, &syntaxErr):
		return "syntax error at byte " + strconv.FormatInt(syntaxErr.Offset, 10)
	case errors.As(err, &typeErr):
		return "field \"" + typeErr.Field + "\" must be a " + typeErr.Type.String()
	default:
		return err.Error() // e.g. `json: unknown field "x"`
	}
}
