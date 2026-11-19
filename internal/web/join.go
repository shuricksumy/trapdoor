package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// Nodes join by themselves: a node sends a join token (one-time, made in the
// UI, or the TD_JOIN_SECRET of this web process for nodes started with it),
// its address, certificate fingerprint and agent token. The web UI then
// connects to it with that pinned certificate, as for a node added by hand.

const (
	joinTokenTTL = 24 * time.Hour
	maxJoinTTL   = 30 * 24 * time.Hour
)

func joinHash(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// handleJoinToken makes a one-time join token (shown once).
func (s *Server) handleJoinToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name  string `json:"name"`
		Hours int    `json:"hours"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	if req.Name != "" && !hostPattern.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "name: lower-case letters, digits and dashes, starting with a letter, at most 31")
		return
	}
	ttl := joinTokenTTL
	if req.Hours > 0 {
		ttl = min(time.Duration(req.Hours)*time.Hour, maxJoinTTL)
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	tok := "tdj_" + base64.RawURLEncoding.EncodeToString(b)
	now := s.Now()
	if err := s.Stats.DB.CreateJoinToken(r.Context(), joinHash(tok), req.Name, now.Unix(), now.Add(ttl).Unix()); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "node.token", req.Name)
	httpx.WriteJSON(w, http.StatusCreated, api.JoinToken{Token: tok, Name: req.Name, ExpiresAt: now.Add(ttl).UTC()})
}

// handleJoin is called by a node (no login: the join token is the proof).
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if s.Hosts == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "nodes are not available")
		return
	}
	var req api.JoinRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if !hostPattern.MatchString(req.Name) {
		httpx.WriteError(w, http.StatusBadRequest, "name: lower-case letters, digits and dashes, starting with a letter, at most 31")
		return
	}
	fp, err := agent.NormalizeFingerprint(req.Fingerprint)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := r.Context()
	// Allowed: the same node again (its certificate is pinned already), the
	// shared secret, or an unused token.
	useToken := false
	if old, ok := s.Hosts.HostFingerprint(req.Name); ok && old == fp {
		// a restart of a known node
	} else if s.JoinSecret != "" && subtle.ConstantTimeCompare([]byte(req.Token), []byte(s.JoinSecret)) == 1 {
		// started with this web UI's TD_JOIN_SECRET
	} else if ok, err := s.Stats.DB.JoinTokenFor(ctx, joinHash(req.Token), req.Name, s.Now().Unix()); err == nil && ok {
		useToken = true
	} else {
		s.Log.Warn("node join refused", "node", req.Name, "ip", s.clientIP(r))
		httpx.WriteError(w, http.StatusUnauthorized, "invalid, used or expired join token")
		return
	}
	st, err := s.Hosts.AddHost(ctx, api.Host{Name: req.Name, URL: req.URL, Token: req.AgentToken, Fingerprint: fp})
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if useToken {
		_, _ = s.Stats.DB.UseJoinToken(ctx, joinHash(req.Token), s.Now().Unix())
	}
	s.Log.Info("node joined", "node", st.Name, "url", st.URL, "machine", st.Machine)
	httpx.WriteJSON(w, http.StatusCreated, st)
}
