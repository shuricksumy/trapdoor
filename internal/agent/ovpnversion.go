package agent

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"github.com/shuricksumy/trapdoor/internal/ccdnet"
)

var ovpnVersionRE = regexp.MustCompile(`OpenVPN (\d+)\.(\d+)`)

// defaultTopology is the topology OpenVPN uses when server.conf has none:
// subnet from 2.7 on, net30 before. It follows the installed openvpn
// binary (the same image runs OpenVPN in Docker); net30 when unknown.
func (s *Server) defaultTopology() ccdnet.Topology {
	s.topoOnce.Do(func() {
		s.topo = ccdnet.TopologyNet30
		if s.cfg.OpenVPNBin == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		out, _ := exec.CommandContext(ctx, s.cfg.OpenVPNBin, "--version").Output()
		if topologyFor(string(out)) == ccdnet.TopologySubnet {
			s.topo = ccdnet.TopologySubnet
		}
	})
	return s.topo
}

// topologyFor reads `openvpn --version` output.
func topologyFor(version string) ccdnet.Topology {
	m := ovpnVersionRE.FindStringSubmatch(version)
	if m == nil {
		return ccdnet.TopologyNet30
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major > 2 || (major == 2 && minor >= 7) {
		return ccdnet.TopologySubnet
	}
	return ccdnet.TopologyNet30
}
