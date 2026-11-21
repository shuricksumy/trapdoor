package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/serverconf"
)

// OpenVPN servers added in trapdoor (besides the first one from the TD_*
// settings) live in TD_OPENVPN_SERVERS_DIR/<id>/, each with its own PKI,
// ccd, management port and tun device named after the id. They are set up
// with the same wizard; port and protocol are chosen when adding them.

// ovpnStateFile is trapdoor's state in the server's directory.
const ovpnStateFile = "trapdoor.json"

// ovpnState is what trapdoor keeps about an added OpenVPN server; the rest
// is in its server.conf.
type ovpnState struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Port  int    `json:"port"`
	Proto string `json:"proto"` // udp or tcp
	// Mgmt is the management interface address (127.0.0.1:<port>).
	Mgmt string `json:"mgmt"`
}

// ovpnConfig is the agent configuration of an added server: the shared
// settings with the server's own directories and management address.
func ovpnConfig(base config.Agent, st ovpnState) config.Agent {
	c := base
	dir := filepath.Join(base.OpenVPNServersDir, st.ID)
	c.OpenVPNDir = dir
	c.PKIDir = filepath.Join(dir, "pki")
	c.CCDDir = filepath.Join(dir, "ccd")
	c.MgmtAddr = st.Mgmt
	// The CRL copy next to server.conf, readable after OpenVPN drops root.
	c.CRLCopies = []string{filepath.Join(dir, "crl.pem")}
	c.SetupPort, c.SetupProto, c.SetupPublicPort = st.Port, st.Proto, 0
	return c
}

// newOpenVPNServer returns the added server described by st; p is its PKI
// (nil before setup).
func newOpenVPNServer(base config.Agent, log *slog.Logger, st ovpnState, p pki.PKI) *Server {
	s := newServer(st.ID, st.Name, ovpnConfig(base, st), log, p)
	s.dev, s.fwChain, s.fwIface = st.ID, "TD-"+st.ID, st.ID
	s.extra = &st
	return s
}

// LoadOpenVPN returns the OpenVPN servers added in trapdoor.
func LoadOpenVPN(cfg config.Agent, log *slog.Logger) ([]*Server, error) {
	if cfg.OpenVPNServersDir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(cfg.OpenVPNServersDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Server
	for _, e := range entries {
		if !e.IsDir() || !validOVPNID(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cfg.OpenVPNServersDir, e.Name(), ovpnStateFile))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var st ovpnState
		if err := json.Unmarshal(data, &st); err != nil {
			return nil, fmt.Errorf("%s/%s: %w", e.Name(), ovpnStateFile, err)
		}
		if st.ID != e.Name() {
			return nil, fmt.Errorf("%s/%s: id %q does not match the directory", e.Name(), ovpnStateFile, st.ID)
		}
		c := ovpnConfig(cfg, st)
		var p pki.PKI
		store, err := pki.Open(c.PKIDir, pki.Options{CAPassphrase: c.CAPassphrase, CRLCopies: c.CRLCopies})
		switch {
		case err == nil:
			p = store
		case !errors.Is(err, pki.ErrNotInitialized):
			return nil, fmt.Errorf("OpenVPN server %s: %w", st.ID, err)
		}
		out = append(out, newOpenVPNServer(cfg, log, st, p))
	}
	return out, nil
}

// validOVPNID: the id is the tun device name. Names starting with "tun"
// are left to the first server (its access rules cover tun+).
func validOVPNID(id string) bool {
	return wgIDPattern.MatchString(id) && !strings.HasPrefix(id, "tun") && id != DefaultServerID
}

// createOpenVPN validates req; the management port is picked by the Host.
func createOpenVPN(req api.CreateServer) (ovpnState, error) {
	if !validOVPNID(req.ID) {
		return ovpnState{}, errors.New("id must be a network interface name: lower-case letters, digits and dashes, starting with a letter, at most 15 characters, not starting with \"tun\" (e.g. ovpn-tcp)")
	}
	st := ovpnState{ID: req.ID, Name: strings.TrimSpace(req.Name), Port: req.Port, Proto: req.Proto}
	if st.Name == "" {
		st.Name = st.ID
	}
	if st.Port < 1 || st.Port > 65535 {
		return st, errors.New("port must be 1-65535")
	}
	if st.Proto != "udp" && st.Proto != "tcp" {
		return st, errors.New("proto must be udp or tcp")
	}
	return st, nil
}

// settings reads the server's server.conf.
func (s *Server) settings() (serverconf.Settings, error) {
	data, err := os.ReadFile(s.conf.Path)
	if err != nil {
		return serverconf.Settings{}, err
	}
	return serverconf.ParseDefault(string(data), string(s.defaultTopology())), nil
}

// listen is the port and protocol (udp or tcp) the server uses, if known.
func (s *Server) listen() (int, string, bool) {
	if s.extra != nil {
		return s.extra.Port, s.extra.Proto, true
	}
	sc, err := s.settings()
	if err != nil {
		return 0, "", false
	}
	return sc.Port, strings.TrimRight(sc.Proto, "46"), true
}

// mgmtPort is the port of the server's management interface.
func (s *Server) mgmtPort() int {
	_, p, err := net.SplitHostPort(s.cfg.MgmtAddr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// subnet is the VPN subnet from server.conf (invalid before setup).
func (s *Server) subnet() netip.Prefix {
	sc, err := s.serverConf()
	if err != nil {
		return netip.Prefix{}
	}
	return sc.Subnet
}

// remove takes an added server away: OpenVPN is stopped and the directory
// moved to <servers dir>/.removed/<id>-<time> (it holds the PKI, so it is
// kept for the admin to delete).
func (s *Server) remove(ctx context.Context) (string, error) {
	if s.extra == nil {
		return "", errors.New("the first OpenVPN server cannot be removed")
	}
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	trash := filepath.Join(s.cfg.OpenVPNServersDir, ".removed")
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return "", err
	}
	dst := filepath.Join(trash, s.id+"-"+time.Now().UTC().Format("20060102-150405"))
	for i := 2; ; i++ { // removed twice within a second (e.g. restored in between)
		if _, err := os.Lstat(dst); errors.Is(err, fs.ErrNotExist) {
			break
		}
		dst = filepath.Join(trash, fmt.Sprintf("%s-%s-%d", s.id, time.Now().UTC().Format("20060102-150405"), i))
	}
	// First the firewall (direct mode removes the chain; file mode leaves
	// it to the container, which notices the directory is gone).
	s.fwMu.Lock()
	s.firewallTarget().remove(ctx)
	s.shaper().remove(ctx)
	s.fwMu.Unlock()
	if err := os.Rename(s.cfg.OpenVPNDir, dst); err != nil {
		return "", err
	}
	// Without server.conf the container's supervisor does not start it again.
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.mgmt.Signal(sctx, "SIGTERM"); err != nil {
		s.log.Info("remove: OpenVPN did not answer on management", "err", err)
	}
	return dst, nil
}
