package agent

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/clientauth"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/pki"
)

func (s *Server) registerAuthRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/certs/{name}/auth", s.handleGetClientAuth)
	mux.HandleFunc("PUT /v1/certs/{name}/auth", s.handleUpdateClientAuth)
}

func toClientAuth(st clientauth.Settings) api.ClientAuth {
	return api.ClientAuth{
		TOTP:         len(st.TOTPKey) > 0,
		TOTPUser:     st.TOTPUser,
		Static:       st.HasStatic(),
		LegacyStatic: st.StaticPlain != "" && st.StaticHash == "",
	}
}

func (s *Server) handleGetClientAuth(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	st, err := s.auth.Get(name)
	if err != nil {
		s.log.Error("read ccd", "name", name, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toClientAuth(st))
}

func (s *Server) handleUpdateClientAuth(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	var req api.ClientAuthUpdate
	if !decodeBody(w, r, &req) {
		return
	}
	switch req.TOTP {
	case "", "enable", "reset", "disable":
	default:
		httpx.WriteError(w, http.StatusBadRequest, `totp must be "enable", "reset", "disable" or empty`)
		return
	}
	if req.StaticPassword != "" && req.RemoveStatic {
		httpx.WriteError(w, http.StatusBadRequest, "set or remove the static password, not both")
		return
	}
	// Only clients that exist (and are not revoked) can get a second factor.
	if s.pki != nil {
		c, err := s.pki.Get(r.Context(), name)
		if err != nil {
			s.pkiError(w, err)
			return
		}
		if c.Status == pki.StatusRevoked && req.TOTP != "disable" && !req.RemoveStatic {
			httpx.WriteError(w, http.StatusConflict, name+" is revoked")
			return
		}
	}

	var newKey []byte
	st, err := s.auth.Update(name, func(st *clientauth.Settings) error {
		switch req.TOTP {
		case "enable", "reset":
			if req.TOTP == "reset" || len(st.TOTPKey) == 0 {
				key, err := clientauth.NewSecret()
				if err != nil {
					return err
				}
				st.TOTPKey, newKey = key, key
			}
			if st.TOTPUser == "" {
				st.TOTPUser = name
			}
		case "disable":
			st.TOTPKey, st.TOTPUser = nil, ""
		}
		if req.RemoveStatic {
			st.StaticPlain, st.StaticHash = "", ""
		}
		if req.StaticPassword != "" {
			h, err := clientauth.HashStatic(req.StaticPassword)
			if err != nil {
				return fmt.Errorf("%w: %v", errBadInput, err)
			}
			st.StaticPlain, st.StaticHash = "", h
		}
		return nil
	})
	if errors.Is(err, errBadInput) {
		httpx.WriteError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), errBadInput.Error()+": "))
		return
	}
	if err != nil {
		s.log.Error("write ccd", "name", name, "err", err)
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := toClientAuth(st)
	if newKey != nil {
		out.Secret = clientauth.Base32(newKey)
		out.URI = clientauth.URI(s.cfg.TOTPIssuer, st.TOTPUser, newKey)
	}
	s.log.Info("client 2FA updated", "name", name, "totp", out.TOTP, "static", out.Static)
	httpx.WriteJSON(w, http.StatusOK, out)
}

var errBadInput = errors.New("bad input")
