package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/authz"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Error is an error with an HTTP status. Handlers return it as a plain error;
// the strict server's error handler renders it.
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

func errStatus(status int, msg string) error { return &Error{Status: status, Msg: msg} }

var (
	errUnauthorized = errStatus(http.StatusUnauthorized, "not signed in")
	errNotFound     = errStatus(http.StatusNotFound, "not found")
)

func badRequest(msg string) error { return errStatus(http.StatusBadRequest, msg) }

// toHTTP maps known errors to statuses.
func toHTTP(err error) (int, string) {
	var e *Error
	var re *tunnel.RemoteError
	switch {
	case errors.As(err, &re):
		if strings.Contains(re.Msg, "no such file or directory") {
			return http.StatusNotFound, re.Msg
		}
		return http.StatusBadRequest, re.Msg
	case errors.Is(err, sandbox.ErrUnreachable):
		return http.StatusServiceUnavailable, unreachableMessage(err)
	case errors.Is(err, sandbox.ErrNoCapacity):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, tunnel.ErrNotConnected):
		return http.StatusConflict, "the agent is not running; start it first"
	case errors.As(err, &e):
		return e.Status, e.Msg
	case errors.Is(err, authz.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, authz.ErrForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, sandbox.ErrNotFound):
		return http.StatusNotFound, "not found"
	case errors.Is(err, sandbox.ErrConflict):
		return http.StatusConflict, err.Error()
	}
	return http.StatusInternalServerError, "internal error"
}

func writeError(w http.ResponseWriter, r *http.Request, err error) {
	status, msg := toHTTP(err)
	if status == http.StatusInternalServerError {
		slog.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// unreachableMessage keeps the first line of a node error, which names the node.
func unreachableMessage(err error) string {
	msg, _, _ := strings.Cut(err.Error(), "\n")
	return msg
}
