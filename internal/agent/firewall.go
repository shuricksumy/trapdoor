package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/ccdnet"
	"github.com/shuricksumy/trapdoor/internal/firewall"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// Firewall modes (TD_FIREWALL).
const (
	FirewallDirect = "direct" // the agent runs iptables-restore itself
	FirewallFile   = "file"   // the OpenVPN container applies the rules file
	FirewallOff    = "off"
)

func (s *Server) registerFirewallRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /firewall", s.handleGetFirewall)
	mux.HandleFunc("PUT /firewall", s.handleSetFirewall)
	mux.HandleFunc("POST /firewall/addresses", s.handleAssignAddresses)
}

func (s *Server) fwPath(ext string) string {
	return filepath.Join(s.cfg.OpenVPNDir, "trapdoor-firewall."+ext)
}

func (s *Server) firewallMode() string {
	if s.cfg.Firewall == "" {
		return FirewallDirect
	}
	return s.cfg.Firewall
}

// loadPolicy reads the saved policy; ok is false when there is none.
func (s *Server) loadPolicy() (p firewall.Policy, ok bool, err error) {
	data, err := os.ReadFile(s.fwPath("json"))
	if errors.Is(err, fs.ErrNotExist) {
		p.Default = firewall.Allow
		firewall.Normalize(&p)
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return p, false, fmt.Errorf("%s: %w", s.fwPath("json"), err)
	}
	firewall.Normalize(&p)
	return p, true, nil
}

// firewallState renders the policy; it does not apply anything.
func (s *Server) firewallState(p firewall.Policy) (api.Firewall, firewall.Result, error) {
	srv, err := s.serverConf()
	if err != nil {
		return api.Firewall{}, firewall.Result{}, err
	}
	ips, err := s.staticIPs()
	if err != nil {
		return api.Firewall{}, firewall.Result{}, err
	}
	res := firewall.RenderChain(s.fwChain, p, firewall.Clients{IPs: ips, ClientToClient: srv.ClientToClient})
	st := api.Firewall{Policy: p, Mode: s.firewallMode(), Warnings: res.Warnings, Missing: res.Missing, Rules: res.Rules, ClientToClient: srv.ClientToClient}
	if st.Warnings == nil {
		st.Warnings = []string{}
	}
	if st.Missing == nil {
		st.Missing = []string{}
	}
	if srv.Subnet.IsValid() {
		st.Subnet = srv.Subnet.String()
	}
	return st, res, nil
}

// applyFirewall renders the saved policy and makes it active. With no saved
// policy nothing is touched (trapdoor never adds rules nobody asked for).
func (s *Server) applyFirewall(ctx context.Context) (api.Firewall, error) {
	s.fwMu.Lock()
	defer s.fwMu.Unlock()
	p, saved, err := s.loadPolicy()
	if err != nil {
		return api.Firewall{}, err
	}
	st, res, err := s.firewallState(p)
	if err != nil {
		return st, err
	}
	if !saved || st.Mode == FirewallOff {
		st.Applied = !saved
		if st.Mode == FirewallOff && p.Enabled {
			st.Error = "the agent does not manage the firewall (TD_FIREWALL=off)"
		}
		return st, nil
	}
	fw := s.firewallTarget()
	if st.Applied, st.Error, err = fw.apply(ctx, res.Rules, true); err != nil {
		return st, err
	}
	if st.Error != "" {
		s.log.Error("firewall rules not applied", "err", st.Error)
	} else if st.Applied {
		s.log.Info("firewall rules applied", "enabled", p.Enabled, "rules", len(p.Rules))
	}
	return st, nil
}

// firewallTarget is the TD-FWD chain for the tun devices. Called with fwMu held.
func (s *Server) firewallTarget() *fwTarget {
	if s.fw == nil {
		s.fw = &fwTarget{mode: s.firewallMode(), chain: s.fwChain, iface: s.fwIface,
			rulesPath: s.fwPath("rules"), statusPath: s.fwPath("status"), iptablesDir: s.cfg.IPTablesDir}
	}
	s.fw.wait = s.firewallWait()
	return s.fw
}

func (s *Server) firewallWait() time.Duration {
	if s.restartWait > 0 {
		return s.restartWait
	}
	return 10 * time.Second
}

func (s *Server) handleGetFirewall(w http.ResponseWriter, r *http.Request) {
	s.fwMu.Lock()
	p, saved, err := s.loadPolicy()
	var st api.Firewall
	if err == nil {
		st, _, err = s.firewallState(p)
	}
	if err == nil {
		if !saved {
			st.Applied = true
		} else if st.Mode != FirewallOff {
			st.Applied, st.Error = s.firewallTarget().state(st.Rules)
		}
	}
	s.fwMu.Unlock()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleSetFirewall(w http.ResponseWriter, r *http.Request) {
	var p firewall.Policy
	if !decodeBody(w, r, &p) {
		return
	}
	firewall.Normalize(&p)
	if err := firewall.Validate(p, pki.ValidName); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.firewallMode() == FirewallOff {
		httpx.WriteError(w, http.StatusConflict, "the agent does not manage the firewall (TD_FIREWALL=off)")
		return
	}
	data, _ := json.MarshalIndent(p, "", "  ")
	s.fwMu.Lock()
	err := writeAtomic(s.fwPath("json"), append(data, '\n'), 0o600)
	s.fwMu.Unlock()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("firewall policy saved", "enabled", p.Enabled, "default", p.Default, "rules", len(p.Rules))
	st, err := s.applyFirewall(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// handleAssignAddresses gives clients a fixed VPN address so rules can
// match them. They get it at their next connect.
func (s *Server) handleAssignAddresses(w http.ResponseWriter, r *http.Request) {
	var req api.AssignAddresses
	if !decodeBody(w, r, &req) {
		return
	}
	if len(req.Names) == 0 || len(req.Names) > 500 {
		httpx.WriteError(w, http.StatusBadRequest, "names: 1-500 clients")
		return
	}
	srv, err := s.serverConf()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.netMu.Lock()
	err = func() error {
		ips, err := s.staticIPs()
		if err != nil {
			return err
		}
		for _, name := range req.Names {
			if !pki.ValidName(name) {
				return fmt.Errorf("%w: %q is not a client name", ccdnet.ErrInvalid, name)
			}
			if _, ok := ips[name]; ok {
				continue
			}
			a, err := ccdnet.FreeAddress(srv, ips)
			if err != nil {
				return err
			}
			if err := s.ccd.Edit(name, func(cur string) (string, error) {
				n, err := ccdnet.Parse(cur)
				if err != nil {
					return "", err
				}
				n.StaticIP = a
				return ccdnet.Apply(cur, n, srv), nil
			}); err != nil {
				return err
			}
			ips[name] = a
			s.log.Info("fixed VPN address assigned", "name", name, "ip", a)
		}
		return nil
	}()
	s.netMu.Unlock()
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, ccdnet.ErrInvalid) {
			status = http.StatusBadRequest
		}
		httpx.WriteError(w, status, err.Error())
		return
	}
	st, err := s.applyFirewall(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

// refreshFirewall re-renders after client addresses changed.
func (s *Server) refreshFirewall(ctx context.Context) {
	if _, err := s.applyFirewall(ctx); err != nil {
		s.log.Warn("firewall refresh", "err", err)
	}
}

// ApplyFirewallAtStart restores the saved rules (direct mode: the kernel
// forgets them on reboot; file mode: the container re-applies by itself).
func (s *Server) ApplyFirewallAtStart(ctx context.Context) {
	if s.firewallMode() != FirewallDirect {
		return
	}
	s.shaper().refresh(ctx)
	if _, saved, err := s.loadPolicy(); err != nil || !saved {
		return
	}
	s.refreshFirewall(ctx)
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".td-tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
