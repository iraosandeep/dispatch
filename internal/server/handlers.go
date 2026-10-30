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
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	var (
		req        notifyRequest
		attachment *notification.Attachment
		ok         bool
	)
	switch {
	case err == nil && mt == "application/json":
		req, ok = s.decodeJSON(w, r)
	case err == nil && mt == "multipart/form-data":
		// Uploads are buffered in memory; bound how many run at once.
		select {
		case s.uploadSem <- struct{}{}:
			defer func() { <-s.uploadSem }()
		default:
			w.Header().Set("Retry-After", "5")
			writeError(w, http.StatusTooManyRequests, notification.CodeRateLimited, "too many uploads in progress; retry shortly")
			return
		}
		req, attachment, ok = s.decodeMultipart(w, r)
	default:
		writeError(w, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType,
			"Content-Type must be application/json, or multipart/form-data to upload a file")
		return
	}
	if !ok {
		return
	}

	priority, err := notification.ParsePriority(req.Priority)
	if err != nil {
		writeNotificationError(w, err)
		return
	}

	timeout := s.opts.DeliveryTimeout
	if attachment != nil {
		timeout = max(timeout, s.opts.UploadTimeout)
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	err = s.opts.Service.Notify(ctx, notification.Message{
		Title:      req.Title,
		Body:       req.Message,
		Priority:   priority,
		Emoji:      req.Emoji,
		Attachment: attachment,
	})
	if err != nil {
		writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, successResponse{OK: true, Message: "Notification accepted"})
}

// decodeJSON reads a single JSON notifyRequest. On failure it writes the
// error response and returns false.
func (s *Server) decodeJSON(w http.ResponseWriter, r *http.Request) (notifyRequest, bool) {
	var req notifyRequest
	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(&req); err != nil {
		if isTooLarge(err) {
			writeError(w, http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
				"request body exceeds "+strconv.FormatInt(s.opts.MaxBodyBytes, 10)+" bytes")
			return req, false
		}
		writeError(w, http.StatusBadRequest, notification.CodeInvalidRequest, "invalid JSON body: "+jsonErrorMessage(err))
		return req, false
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, notification.CodeInvalidRequest, "request body must contain a single JSON object")
		return req, false
	}
	return req, true
}

// decodeMultipart reads text fields (message, title, priority, emoji) and one
// optional "file" part, streaming parts with explicit size limits.
func (s *Server) decodeMultipart(w http.ResponseWriter, r *http.Request) (notifyRequest, *notification.Attachment, bool) {
	var req notifyRequest
	var att *notification.Attachment

	fail := func(status int, code notification.Code, msg string) (notifyRequest, *notification.Attachment, bool) {
		writeError(w, status, code, msg)
		return req, nil, false
	}
	tooLarge := func() (notifyRequest, *notification.Attachment, bool) {
		return fail(http.StatusRequestEntityTooLarge, CodePayloadTooLarge,
			"request body exceeds "+strconv.FormatInt(s.opts.MaxUploadBytes, 10)+" bytes")
	}

	r.Body = http.MaxBytesReader(w, r.Body, s.opts.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "invalid multipart body: "+err.Error())
	}

	fields := map[string]*string{"message": &req.Message, "title": &req.Title, "priority": &req.Priority, "emoji": &req.Emoji}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			if isTooLarge(err) {
				return tooLarge()
			}
			return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "invalid multipart body")
		}

		name := part.FormName()
		if dst, ok := fields[name]; ok {
			if part.FileName() != "" {
				return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "field \""+name+"\" must be text, not a file")
			}
			data, err := io.ReadAll(io.LimitReader(part, s.opts.MaxBodyBytes+1))
			if err != nil {
				if isTooLarge(err) {
					return tooLarge()
				}
				return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "invalid multipart body")
			}
			if int64(len(data)) > s.opts.MaxBodyBytes {
				return fail(http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "field \""+name+"\" is too large")
			}
			*dst = string(data)
			continue
		}
		if name != "file" {
			return fail(http.StatusBadRequest, notification.CodeInvalidRequest,
				"unknown field \""+name+"\"; allowed: message, title, priority, emoji, file")
		}
		if att != nil {
			return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "only one file may be attached")
		}
		filename := part.FileName() // already reduced to a base name
		if filename == "" || filename == "." || filename == "/" {
			return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "the file part needs a filename")
		}
		data, err := io.ReadAll(io.LimitReader(part, notification.MaxAttachmentBytes+1))
		if err != nil {
			if isTooLarge(err) {
				return tooLarge()
			}
			return fail(http.StatusBadRequest, notification.CodeInvalidRequest, "invalid multipart body")
		}
		att = &notification.Attachment{Name: filename, Data: data}
	}
	return req, att, true
}

func isTooLarge(err error) bool {
	var tooLarge *http.MaxBytesError
	return errors.As(err, &tooLarge)
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
	case notification.CodeMessageTooLarge, notification.CodeAttachmentTooLarge:
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
