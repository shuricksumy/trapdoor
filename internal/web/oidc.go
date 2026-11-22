package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"

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
	public.HandleFunc("GET /api/v1/auth/methods", func(w http.ResponseWriter, r *http.Request) {
		m := LoginMethods{Password: s.passwordAllowed(r)}
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
		s.auditAs(r, id.Username, "oidc", "login.failed", "not in an allowed group")
		s.loginError(w, r, id.Username+" is not in a group allowed to sign in here")
		return
	case err != nil:
		s.Log.Error("sign-in failed", "ip", ip, "err", err)
		s.loginError(w, r, "the sign-in could not be checked")
		return
	}
	u, err := s.oidcAccount(r.Context(), id)
	if err != nil {
		s.Log.Warn("sign-in refused", "ip", ip, "user", id.Username, "err", err)
		s.auditAs(r, id.Username, "oidc", "login.failed", err.Error())
		msg := "the account could not be used"
		switch {
		case errors.Is(err, auth.ErrInvalidCredentials):
			msg = "the account " + id.Username + " is disabled here"
		case s.OIDCUser != "":
			msg = "TD_OIDC_USER " + s.OIDCUser + " is not an account here"
		}
		s.loginError(w, r, msg)
		return
	}
	tok, exp, err := s.Auth.StartSessionVia(r.Context(), u, ip, r.UserAgent(), "oidc")
	if err != nil {
		s.Log.Error("start session", "err", err)
		s.loginError(w, r, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode})
	s.Log.Info("login", "ip", ip, "username", u.Username, "role", u.Role, "via", "oidc", "oidc_user", id.Username, "oidc_email", id.Email)
	s.auditAs(r, u.Username, "oidc", "login", id.Email)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// oidcAccount is the account a sign-in becomes: the person's own (made at
// their first sign-in, its role from their groups: an admin group makes an
// admin, else a viewer), or, with TD_OIDC_USER, that local account for
// everyone (how it worked before accounts per person).
func (s *Server) oidcAccount(ctx context.Context, id oidc.Identity) (auth.User, error) {
	if s.OIDCUser != "" {
		users, err := s.Auth.Users(ctx)
		if err != nil {
			return auth.User{}, err
		}
		for _, u := range users {
			if u.Username == s.OIDCUser {
				if u.Disabled {
					return auth.User{}, auth.ErrInvalidCredentials
				}
				return u, nil
			}
		}
		return auth.User{}, auth.ErrUserNotFound
	}
	role := auth.RoleViewer
	if len(s.OIDCAdminGroups) == 0 || slices.ContainsFunc(id.Groups, func(g string) bool { return slices.Contains(s.OIDCAdminGroups, g) }) {
		role = auth.RoleAdmin
	}
	return s.Auth.OIDCAccount(ctx, id.Subject, id.Username, role)
}

// loginError sends the browser back to the login page with a message.
func (s *Server) loginError(w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusSeeOther)
}
