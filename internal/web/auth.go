package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

const cookieName = "td_session"

type ctxKey struct{}

// Principal is who made a request: an admin user, or the API token.
type Principal struct {
	Username string `json:"username"`
	Method   string `json:"method"` // "session" or "token"
}

// principal returns the authenticated caller of r (set by requireAuth).
func principal(r *http.Request) Principal {
	p, _ := r.Context().Value(ctxKey{}).(Principal)
	return p
}

func (s *Server) registerAuthRoutes(public, protected *http.ServeMux) {
	public.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	protected.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	protected.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	protected.HandleFunc("POST /api/v1/auth/password", s.handleChangePassword)
}

// requireAuth accepts a login session cookie or "Authorization: Bearer
// <TD_WEB_API_TOKEN>" (for scripts). Cookie requests that change state must
// come from the same site.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	token := []byte(s.APIToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			if len(token) == 0 || subtle.ConstantTimeCompare([]byte(got), token) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="trapdoor"`)
				httpx.WriteError(w, http.StatusUnauthorized, "invalid API token")
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, Principal{Username: "api-token", Method: "token"})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		c, err := r.Cookie(cookieName)
		if err != nil || s.Auth == nil {
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		sess, err := s.Auth.Session(r.Context(), c.Value)
		if err != nil {
			if !errors.Is(err, auth.ErrNoSession) {
				s.Log.Error("session lookup", "err", err)
			}
			s.clearCookie(w)
			httpx.WriteError(w, http.StatusUnauthorized, "login required")
			return
		}
		if !sameOrigin(r) {
			httpx.WriteError(w, http.StatusForbidden, "cross-site request refused")
			return
		}
		ctx := context.WithValue(r.Context(), ctxKey{}, Principal{Username: sess.User.Username, Method: "session"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// sameOrigin is the CSRF check for state-changing requests (on top of
// SameSite=Strict cookies): Fetch Metadata if present, else Origin vs Host.
func sameOrigin(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return true
	case "":
	default:
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // non-browser client; the cookie is SameSite=Strict anyway
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth == nil {
		httpx.WriteError(w, http.StatusServiceUnavailable, "login not available")
		return
	}
	if !sameOrigin(r) {
		httpx.WriteError(w, http.StatusForbidden, "cross-site request refused")
		return
	}
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ip := s.clientIP(r)
	if ok, wait := s.Limiter.Allowed(ip, req.Username); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		s.Log.Warn("login throttled", "ip", ip, "username", req.Username)
		httpx.WriteError(w, http.StatusTooManyRequests, "too many failed logins, try again later")
		return
	}
	u, err := s.Auth.Authenticate(r.Context(), req.Username, req.Password)
	if err != nil {
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			s.Log.Error("login", "err", err)
			httpx.WriteError(w, http.StatusInternalServerError, "internal error")
			return
		}
		s.Limiter.Fail(ip, req.Username)
		s.Log.Warn("login failed", "ip", ip, "username", req.Username)
		httpx.WriteError(w, http.StatusUnauthorized, auth.ErrInvalidCredentials.Error())
		return
	}
	s.Limiter.Succeed(req.Username)
	tok, exp, err := s.Auth.StartSession(r.Context(), u, ip, r.UserAgent())
	if err != nil {
		s.Log.Error("start session", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: tok, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
	s.Log.Info("login", "ip", ip, "username", u.Username)
	httpx.WriteJSON(w, http.StatusOK, Principal{Username: u.Username, Method: "session"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.Auth.EndSession(r.Context(), c.Value)
	}
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, principal(r))
}

type changePasswordRequest struct {
	Current string `json:"current_password"`
	New     string `json:"new_password"`
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if p.Method != "session" {
		httpx.WriteError(w, http.StatusForbidden, "log in as a user to change a password")
		return
	}
	var req changePasswordRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := s.Auth.Authenticate(r.Context(), p.Username, req.Current); err != nil {
		httpx.WriteError(w, http.StatusForbidden, "current password is wrong")
		return
	}
	if err := s.Auth.SetPassword(r.Context(), p.Username, req.New); err != nil {
		if errors.Is(err, auth.ErrWeakPassword) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.Log.Error("set password", "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.Log.Info("password changed; all sessions of the user ended", "username", p.Username)
	s.clearCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: "", Path: "/", MaxAge: -1, Expires: time.Unix(0, 0),
		HttpOnly: true, Secure: s.CookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

// clientIP is the peer address, or the X-Forwarded-For client when the peer
// is a trusted reverse proxy (TD_TRUSTED_PROXIES).
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || !s.trusted(addr) {
		return host
	}
	// Walk X-Forwarded-For from the right; the first untrusted hop is the client.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		if !s.trusted(a) {
			return a.String()
		}
	}
	return host
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.TrustedProxies {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

const maxBody = 64 << 10

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// securityHeaders sets defensive headers on every response. With framing
// allowed (TD_FRAME_ANCESTORS), the CSP's frame-ancestors decides instead of
// X-Frame-Options (which cannot name other origins).
func securityHeaders(next http.Handler, allowFrames bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		if !allowFrames {
			h.Set("X-Frame-Options", "DENY")
		}
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
