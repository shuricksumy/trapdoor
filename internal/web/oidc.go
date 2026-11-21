package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/oidc"
)

// Sign-in with an OpenID Connect provider (TD_OIDC_*, e.g. Authelia). It
// signs in as a local admin: there are no separate OIDC users. The
// password login stays, for when the provider is down.

// OIDC is what the web part needs from the provider (*oidc.Provider).
type OIDC interface {
	Start(ctx context.Context) (state, to string, err error)
	Finish(ctx context.Context, cookieState, state, code string) (oidc.Identity, error)
}

const oidcCookie = "td_oidc"

// LoginMethods is GET /api/v1/auth/methods (no login needed): what the
// login page shows.
type LoginMethods struct {
	Password bool   `json:"password"`
	OIDC     string `json:"oidc,omitempty"` // the button's name; empty = off
}

func (s *Server) registerOIDCRoutes(public *http.ServeMux) {
	public.HandleFunc("GET /api/v1/auth/methods", func(w http.ResponseWriter, _ *http.Request) {
		m := LoginMethods{Password: true}
		if s.OIDC != nil {
			m.OIDC = s.OIDCName
			if m.OIDC == "" {
				m.OIDC = "single sign-on"
			}
		}
		httpx.WriteJSON(w, http.StatusOK, m)
	})
	public.HandleFunc("GET /api/v1/auth/oidc", s.handleOIDCStart)
	public.HandleFunc("GET /api/v1/auth/oidc/callback", s.handleOIDCCallback)
}

func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || s.Auth == nil {
		httpx.WriteError(w, http.StatusNotFound, "single sign-on is not set up")
		return
	}
	state, to, err := s.OIDC.Start(r.Context())
	if err != nil {
		s.Log.Error("sign-in: provider", "err", err)
		s.loginError(w, r, "the sign-in provider cannot be reached")
		return
	}
	// Lax: the browser comes back from the provider with a top-level GET.
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state, Path: "/api/v1/auth/oidc", MaxAge: 600,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, to, http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.OIDC == nil || s.Auth == nil {
		httpx.WriteError(w, http.StatusNotFound, "single sign-on is not set up")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc", MaxAge: -1,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteLaxMode})
	q := r.URL.Query()
	ip := s.clientIP(r)
	if e := q.Get("error"); e != "" {
		s.Log.Warn("sign-in refused by the provider", "ip", ip, "error", e, "description", q.Get("error_description"))
		s.loginError(w, r, "the sign-in provider said no: "+e)
		return
	}
	var cookieState string
	if c, err := r.Cookie(oidcCookie); err == nil {
		cookieState = c.Value
	}
	id, err := s.OIDC.Finish(r.Context(), cookieState, q.Get("state"), q.Get("code"))
	switch {
	case errors.Is(err, oidc.ErrState):
		s.loginError(w, r, err.Error())
		return
	case errors.Is(err, oidc.ErrNotAllowed):
		s.Log.Warn("sign-in refused: not in an allowed group", "ip", ip, "user", id.Username, "groups", id.Groups)
		s.loginError(w, r, id.Username+" is not in a group allowed to sign in here")
		return
	case err != nil:
		s.Log.Error("sign-in failed", "ip", ip, "err", err)
		s.loginError(w, r, "the sign-in could not be checked")
		return
	}
	u, err := s.oidcUser(r.Context())
	if err != nil {
		s.Log.Error("sign-in: no local admin", "err", err)
		s.loginError(w, r, "there is no local admin to sign in as: create one with 'trapdoor user add'")
		return
	}
	tok, exp, err := s.Auth.StartSession(r.Context(), u, ip, r.UserAgent())
	if err != nil {
		s.Log.Error("start session", "err", err)
		s.loginError(w, r, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.Log.Info("login", "ip", ip, "username", u.Username, "via", "oidc", "oidc_user", id.Username, "oidc_email", id.Email)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// oidcUser is the local admin a sign-in becomes: TD_OIDC_USER, or the
// first admin created.
func (s *Server) oidcUser(ctx context.Context) (auth.User, error) {
	users, err := s.Auth.Users(ctx)
	if err != nil {
		return auth.User{}, err
	}
	if len(users) == 0 {
		return auth.User{}, errors.New("no admin users")
	}
	first := users[0]
	for _, u := range users {
		if s.OIDCUser != "" && u.Username == s.OIDCUser {
			return u, nil
		}
		if u.ID < first.ID {
			first = u
		}
	}
	if s.OIDCUser != "" {
		return auth.User{}, errors.New("TD_OIDC_USER " + s.OIDCUser + " is not an admin user")
	}
	return first, nil
}

// loginError sends the browser back to the login page with a message.
func (s *Server) loginError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}
