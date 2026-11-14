package api

import (
	"errors"
	"log/slog"
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

// loginFailed sends the browser back to the login page with a message.
func loginFailed(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

func (a *API) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		_ = a.Auth.EndSession(r.Context(), c.Value)
	}
	http.SetCookie(w, a.Auth.SessionCookie(""))
	w.WriteHeader(http.StatusNoContent)
}
