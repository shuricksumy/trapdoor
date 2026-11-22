package web

import (
	"errors"
	"net/http"

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// The Admins section of the Account page: every account, its role, how it
// signs in and when it last did. Admins only (a viewer sees nothing here).

// Account is one row of GET /api/v1/admins.
type Account struct {
	auth.User
	Sessions int  `json:"sessions"` // open sessions now
	You      bool `json:"you,omitempty"`
}

type accountChange struct {
	Role     *string `json:"role"`
	Disabled *bool   `json:"disabled"`
}

type newAccount struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) registerAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admins", s.adminOnly(s.handleAccounts))
	mux.HandleFunc("POST /api/v1/admins", s.adminOnly(s.handleAddAccount))
	mux.HandleFunc("PUT /api/v1/admins/{name}", s.adminOnly(s.handleChangeAccount))
	mux.HandleFunc("POST /api/v1/admins/{name}/password", s.adminOnly(s.handleSetAccountPassword))
	mux.HandleFunc("POST /api/v1/admins/{name}/logout", s.adminOnly(s.handleEndAccountSessions))
	mux.HandleFunc("DELETE /api/v1/admins/{name}", s.adminOnly(s.handleDeleteAccount))
}

// adminOnly: also reads (a viewer does not see the accounts and their addresses).
func (s *Server) adminOnly(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if principal(r).Role != auth.RoleAdmin {
			httpx.WriteError(w, http.StatusForbidden, "admins only")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	users, err := s.Auth.Users(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	counts, err := s.Auth.SessionCounts(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	me := principal(r).Username
	out := make([]Account, 0, len(users))
	for _, u := range users {
		out = append(out, Account{User: u, Sessions: counts[u.ID], You: u.Username == me})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleAddAccount(w http.ResponseWriter, r *http.Request) {
	var in newAccount
	if !decodeJSON(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = auth.RoleAdmin
	}
	u, err := s.Auth.CreateUserRole(r.Context(), in.Username, in.Password, in.Role)
	if err != nil {
		s.accountError(w, err)
		return
	}
	s.audit(r, "admin.add", u.Username+" ("+u.Role+")")
	httpx.WriteJSON(w, http.StatusCreated, Account{User: u})
}

func (s *Server) handleChangeAccount(w http.ResponseWriter, r *http.Request) {
	var in accountChange
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if name == principal(r).Username && in.Disabled != nil && *in.Disabled {
		httpx.WriteError(w, http.StatusConflict, "you cannot turn off your own account; use another admin account")
		return
	}
	if in.Role != nil {
		if err := s.Auth.SetRole(r.Context(), name, *in.Role); err != nil {
			s.accountError(w, err)
			return
		}
		s.audit(r, "admin.role", name+" → "+*in.Role)
	}
	if in.Disabled != nil {
		if err := s.Auth.SetDisabled(r.Context(), name, *in.Disabled); err != nil {
			s.accountError(w, err)
			return
		}
		action := "admin.enable"
		if *in.Disabled {
			action = "admin.disable"
		}
		s.audit(r, action, name)
	}
	s.handleAccounts(w, r)
}

func (s *Server) handleSetAccountPassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if err := s.Auth.SetPassword(r.Context(), name, in.Password); err != nil {
		s.accountError(w, err)
		return
	}
	s.audit(r, "admin.password", name)
	if name == principal(r).Username {
		s.clearCookie(w) // their sessions ended, this one too
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEndAccountSessions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	n, err := s.Auth.EndSessions(r.Context(), name)
	if err != nil {
		s.accountError(w, err)
		return
	}
	s.audit(r, "admin.logout", name)
	httpx.WriteJSON(w, http.StatusOK, map[string]int{"ended": n})
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == principal(r).Username {
		httpx.WriteError(w, http.StatusConflict, "you cannot delete your own account; use another admin account")
		return
	}
	if err := s.Auth.DeleteUser(r.Context(), name); err != nil {
		s.accountError(w, err)
		return
	}
	s.audit(r, "admin.delete", name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) accountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrUserNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrLastAdmin), errors.Is(err, auth.ErrUserExists):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrBadRole), errors.Is(err, auth.ErrInvalidUsername), errors.Is(err, auth.ErrWeakPassword):
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
	}
}
