package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/ovpn"
	"github.com/shuricksumy/tunnelward/internal/pki"
)

const (
	maxBodyBytes    = 64 << 10
	maxValidityDays = 3650
	minPassphrase   = 4 // openssl refuses shorter PEM passphrases
	maxPassphrase   = 1024
)

func (s *Server) registerCertRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/certs", s.withPKI(s.handleListCerts))
	mux.HandleFunc("POST /v1/certs", s.withPKI(s.handleIssueCert))
	mux.HandleFunc("GET /v1/certs/{name}", s.withPKI(s.handleGetCert))
	mux.HandleFunc("DELETE /v1/certs/{name}", s.withPKI(s.handleRemoveCert))
	mux.HandleFunc("POST /v1/certs/{name}/revoke", s.withPKI(s.handleRevokeCert))
	mux.HandleFunc("POST /v1/certs/{name}/unrevoke", s.withPKI(s.handleUnrevokeCert))
	mux.HandleFunc("POST /v1/certs/{name}/renew", s.withPKI(s.handleRenewCert))
	mux.HandleFunc("GET /v1/certs/{name}/ovpn", s.withPKI(s.handleProfile))
	mux.HandleFunc("POST /v1/crl", s.withPKI(s.handleGenCRL))
}

// withPKI answers 503 until a PKI is available (e.g. before setup).
func (s *Server) withPKI(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.pki == nil {
			httpx.WriteError(w, http.StatusServiceUnavailable, "PKI is not initialized")
			return
		}
		h(w, r)
	}
}

func (s *Server) handleListCerts(w http.ResponseWriter, r *http.Request) {
	certs, err := s.pki.List(r.Context())
	if err != nil {
		s.pkiError(w, err)
		return
	}
	out := make([]api.Cert, 0, len(certs))
	for _, c := range certs {
		out = append(out, toAPICert(c))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetCert(w http.ResponseWriter, r *http.Request) {
	c, err := s.pki.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.pkiError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAPICert(c))
}

func (s *Server) handleIssueCert(w http.ResponseWriter, r *http.Request) {
	var req api.IssueCertRequest
	if !decodeBody(w, r, &req) {
		return
	}
	validity, err := validityFromDays(req.ValidityDays)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p := len(req.Passphrase); p != 0 && (p < minPassphrase || p > maxPassphrase) {
		httpx.WriteError(w, http.StatusBadRequest,
			fmt.Sprintf("passphrase must be %d-%d characters", minPassphrase, maxPassphrase))
		return
	}
	c, err := s.pki.Issue(r.Context(), pki.IssueRequest{
		Name:       req.Name,
		Kind:       pki.Kind(req.Kind),
		Passphrase: req.Passphrase,
		Validity:   validity,
	})
	if err != nil {
		s.pkiError(w, err)
		return
	}
	s.log.Info("certificate issued", "name", c.Name, "serial", c.Serial)
	httpx.WriteJSON(w, http.StatusCreated, toAPICert(c))
}

func (s *Server) handleRevokeCert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.pki.Revoke(r.Context(), name); err != nil {
		s.pkiError(w, err)
		return
	}
	s.log.Info("certificate revoked", "name", name)
	s.disconnect(r.Context(), name)
	s.respondCert(w, r, name)
}

func (s *Server) handleUnrevokeCert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.pki.Unrevoke(r.Context(), name); err != nil {
		s.pkiError(w, err)
		return
	}
	s.log.Info("certificate unrevoked", "name", name)
	s.respondCert(w, r, name)
}

func (s *Server) handleRenewCert(w http.ResponseWriter, r *http.Request) {
	var req api.RenewCertRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	validity, err := validityFromDays(req.ValidityDays)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	c, err := s.pki.Renew(r.Context(), r.PathValue("name"), validity)
	if err != nil {
		s.pkiError(w, err)
		return
	}
	s.log.Info("certificate renewed", "name", c.Name, "serial", c.Serial)
	httpx.WriteJSON(w, http.StatusOK, toAPICert(c))
}

func (s *Server) handleRemoveCert(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.pki.Remove(r.Context(), name); err != nil {
		s.pkiError(w, err)
		return
	}
	if err := s.gen.RemoveClient(name); err != nil {
		s.log.Warn("remove client files", "name", name, "err", err)
	}
	s.log.Info("certificate removed", "name", name)
	s.disconnect(r.Context(), name)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b, err := s.pki.Bundle(r.Context(), name)
	if err != nil {
		s.pkiError(w, err)
		return
	}
	profile, err := s.gen.Build(r.Context(), name, b)
	if err != nil {
		s.pkiError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-openvpn-profile")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.ovpn"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(profile)
}

func (s *Server) handleGenCRL(w http.ResponseWriter, r *http.Request) {
	if err := s.pki.GenCRL(r.Context()); err != nil {
		s.pkiError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) respondCert(w http.ResponseWriter, r *http.Request, name string) {
	c, err := s.pki.Get(r.Context(), name)
	if err != nil {
		s.pkiError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toAPICert(c))
}

// pkiError maps PKI errors to HTTP status codes.
func (s *Server) pkiError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, pki.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, pki.ErrInvalidName), errors.Is(err, pki.ErrInvalidKind):
		status = http.StatusBadRequest
	case errors.Is(err, pki.ErrAlreadyExists), errors.Is(err, pki.ErrNotValid), errors.Is(err, pki.ErrNotRevoked):
		status = http.StatusConflict
	case errors.Is(err, pki.ErrNotInitialized), errors.Is(err, ovpn.ErrNoTemplate), errors.Is(err, ovpn.ErrNoServerConf):
		status = http.StatusServiceUnavailable
	}
	if status == http.StatusInternalServerError {
		s.log.Error("pki", "err", err)
	}
	httpx.WriteError(w, status, err.Error())
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func validityFromDays(days int) (time.Duration, error) {
	if days < 0 || days > maxValidityDays {
		return 0, fmt.Errorf("validity_days must be between 1 and %d", maxValidityDays)
	}
	return time.Duration(days) * 24 * time.Hour, nil
}

func toAPICert(c pki.Cert) api.Cert {
	return api.Cert{
		Name:         c.Name,
		Serial:       c.Serial,
		Status:       string(c.Status),
		NotAfter:     c.NotAfter,
		RevokedAt:    c.RevokedAt,
		RevokeReason: c.RevokeReason,
		Current:      c.Current,
		Kind:         string(c.Kind),
	}
}
