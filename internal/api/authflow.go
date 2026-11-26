package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/gempir/agenthold/internal/auth"
)

func (a *API) login(w http.ResponseWriter, r *http.Request) {
	target, err := a.Auth.BeginLogin(r.Context(), chi.URLParam(r, "provider"), r.URL.Query().Get("redirect"))
	if errors.Is(err, auth.ErrUnknownProvider) {
		writeError(w, r, errNotFound)
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "begin login", "err", err)
		loginFailed(w, r, "identity provider unavailable")
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *API) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		loginFailed(w, r, "sign-in was cancelled or denied ("+e+")")
		return
	}
	token, redirect, err := a.Auth.FinishLogin(r.Context(), chi.URLParam(r, "provider"), q.Get("code"), q.Get("state"), r.UserAgent())
	switch {
	case errors.Is(err, auth.ErrUnknownProvider):
		writeError(w, r, errNotFound)
		return
	case errors.Is(err, auth.ErrInvalidState):
		loginFailed(w, r, err.Error())
		return
	case errors.Is(err, auth.ErrNotAllowed):
		loginFailed(w, r, err.Error())
		return
	case err != nil:
		slog.WarnContext(r.Context(), "login failed", "provider", chi.URLParam(r, "provider"), "err", err)
		loginFailed(w, r, "sign-in failed")
		return
	}
	http.SetCookie(w, a.Auth.SessionCookie(token))
	http.Redirect(w, r, redirect, http.StatusFound)
}

// localLogin signs in the local admin (auth.local) with username and password.
func (a *API) localLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil {
		writeError(w, r, badRequest("expected {\"username\", \"password\"}"))
		return
	}
	if len(in.Username) > 256 || len(in.Password) > 1024 {
		writeError(w, r, errStatus(http.StatusUnauthorized, auth.ErrBadCredentials.Error()))
		return
	}
	client, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		client = r.RemoteAddr
	}
	token, err := a.Auth.LocalLogin(r.Context(), in.Username, in.Password, client, r.UserAgent())
	switch {
	case errors.Is(err, auth.ErrLocalDisabled):
		writeError(w, r, errStatus(http.StatusNotFound, err.Error()))
		return
	case errors.Is(err, auth.ErrBadCredentials):
		slog.WarnContext(r.Context(), "local sign-in failed", "client", client)
		writeError(w, r, errStatus(http.StatusUnauthorized, err.Error()))
		return
	case errors.Is(err, auth.ErrRateLimited):
		slog.WarnContext(r.Context(), "local sign-in rate limited", "client", client)
		w.Header().Set("Retry-After", "300")
		writeError(w, r, errStatus(http.StatusTooManyRequests, err.Error()))
		return
	case err != nil:
		writeError(w, r, err)
		return
	}
	http.SetCookie(w, a.Auth.SessionCookie(token))
	w.WriteHeader(http.StatusNoContent)
}

// loginFailed sends the browser back to the login page with a message.
func loginFailed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(a.Auth.SessionCookieName()); err == nil && c.Value != "" {
		_ = a.Auth.EndSession(r.Context(), c.Value)
	}
	if u := auth.UserFrom(r.Context()); u != nil {
		_ = a.Preview.EndUserSessions(r.Context(), u.ID)
		if a.execGate != nil {
			a.execGate.endUserSessions(u.ID)
		}
		if a.Executor != nil {
			a.Executor.ClearUserPairings(u.ID)
		}
	}
	http.SetCookie(w, a.Auth.SessionCookie(""))
	w.WriteHeader(http.StatusNoContent)
}
