package agent

import (
	"context"
	"net/http"
	"net/netip"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/firewall"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// Access rules for a WireGuard server: the same policy as for OpenVPN,
// rendered into the chain TD-<id> for its interface. Every client has a
// fixed address, so rules always match.

// policyLocked returns the saved policy (ok false: none yet).
func (s *WGServer) policyLocked() (firewall.Policy, bool) {
	if s.st.Firewall == nil {
		p := firewall.Policy{Default: firewall.Allow}
		firewall.Normalize(&p)
		return p, false
	}
	p := *s.st.Firewall
	firewall.Normalize(&p)
	return p, true
}

func (s *WGServer) firewallStateLocked(p firewall.Policy) (api.Firewall, firewall.Result) {
	ips := make(map[string]netip.Addr, len(s.st.Peers))
	for _, peer := range s.st.Peers {
		ips[peer.Name] = peer.Address
	}
	res := firewall.RenderChain(s.fw.chain, p, firewall.Clients{IPs: ips})
	st := api.Firewall{Policy: p, Mode: s.fw.mode, Warnings: res.Warnings, Missing: res.Missing,
		Rules: res.Rules, Subnet: s.subnet().String()}
	if st.Warnings == nil {
		st.Warnings = []string{}
	}
	if st.Missing == nil {
		st.Missing = []string{}
	}
	return st, res
}

// applyFirewallLocked renders and applies the saved policy. Nothing is
// touched before a policy was saved. wait: in file mode, wait for the VPN
// container's answer.
func (s *WGServer) applyFirewallLocked(ctx context.Context, wait bool) (api.Firewall, error) {
	p, saved := s.policyLocked()
	st, res := s.firewallStateLocked(p)
	if !saved || s.fw.mode == FirewallOff {
		st.Applied = !saved
		if s.fw.mode == FirewallOff && p.Enabled {
			st.Error = "the agent does not manage the firewall (TD_FIREWALL=off)"
		}
		return st, nil
	}
	var err error
	if st.Applied, st.Error, err = s.fw.apply(ctx, res.Rules, wait); err != nil {
		return st, err
	}
	if st.Error != "" {
		s.log.Error("firewall rules not applied", "err", st.Error)
	}
	return st, nil
}

func (s *WGServer) refreshFirewallLocked(ctx context.Context) {
	if _, err := s.applyFirewallLocked(ctx, false); err != nil {
		s.log.Warn("firewall refresh", "err", err)
	}
}

func (s *WGServer) handleGetFirewall(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, saved := s.policyLocked()
	st, _ := s.firewallStateLocked(p)
	switch {
	case !saved:
		st.Applied = true
	case s.fw.mode != FirewallOff:
		st.Applied, st.Error = s.fw.state(st.Rules)
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *WGServer) handleSetFirewall(w http.ResponseWriter, r *http.Request) {
	var p firewall.Policy
	if !decodeBody(w, r, &p) {
		return
	}
	firewall.Normalize(&p)
	if err := firewall.Validate(p, pki.ValidName); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.fw.mode == FirewallOff {
		httpx.WriteError(w, http.StatusConflict, "the agent does not manage the firewall (TD_FIREWALL=off)")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.st.Firewall
	s.st.Firewall = &p
	if err := s.save(r.Context()); err != nil {
		s.st.Firewall = old
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("firewall policy saved", "enabled", p.Enabled, "default", p.Default, "rules", len(p.Rules))
	st, err := s.applyFirewallLocked(r.Context(), true)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// handleAssignAddresses: WireGuard clients always have a fixed address, so
// there is nothing to assign; unknown names are an error like for OpenVPN.
func (s *WGServer) handleAssignAddresses(w http.ResponseWriter, r *http.Request) {
	var req api.AssignAddresses
	if !decodeBody(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range req.Names {
		if s.find(n) < 0 {
			httpx.WriteError(w, http.StatusBadRequest, n+" is not a client of this server")
			return
		}
	}
	st, err := s.applyFirewallLocked(r.Context(), true)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}
