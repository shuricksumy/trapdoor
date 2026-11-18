package ccdnet

import (
	"bufio"
	"net/netip"
	"strings"
)

// Server is what the client settings depend on in server.conf.
type Server struct {
	Subnet           netip.Prefix // from "server 10.8.0.0 255.255.255.0"
	Topology         Topology     // "topology", default net30 (OpenVPN's default)
	PoolStart        netip.Addr   // "ifconfig-pool <start> <end>", if set
	PoolEnd          netip.Addr
	Routes           []netip.Prefix // "route" lines (kernel routes into the tunnel)
	ClientConfigDir  bool
	PushesFullTunnel bool // push "redirect-gateway ..." for everyone
	ClientToClient   bool // OpenVPN routes client-to-client traffic itself
}

// ServerIP is the server's own VPN address (first host).
func (s Server) ServerIP() netip.Addr { return s.Subnet.Addr().Next() }

// HasRoute reports whether a "route" line covers p.
func (s Server) HasRoute(p netip.Prefix) bool {
	for _, r := range s.Routes {
		if r.Bits() <= p.Bits() && r.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// ParseServer reads the relevant directives of server.conf, with OpenVPN
// 2.6's default topology (net30) when there is no topology line.
func ParseServer(conf string) Server { return ParseServerDefault(conf, TopologyNet30) }

// ParseServerDefault is ParseServer with the default topology of the OpenVPN
// that runs the file (subnet from 2.7 on).
func ParseServerDefault(conf string, def Topology) Server {
	s := Server{Topology: def}
	sc := bufio.NewScanner(strings.NewReader(conf))
	for sc.Scan() {
		f, ok := directive(sc.Text())
		if !ok {
			continue
		}
		switch f[0] {
		case "server":
			if len(f) >= 3 {
				if p, err := prefix(f[1], f[2]); err == nil {
					s.Subnet = p
				}
			}
		case "topology":
			if len(f) >= 2 {
				s.Topology = Topology(f[1])
			}
		case "route":
			if len(f) >= 3 {
				if p, err := prefix(f[1], f[2]); err == nil {
					s.Routes = append(s.Routes, p)
				}
			}
		case "client-config-dir":
			s.ClientConfigDir = true
		case "client-to-client":
			s.ClientToClient = true
		case "ifconfig-pool":
			if len(f) >= 3 {
				s.PoolStart, _ = netip.ParseAddr(f[1])
				s.PoolEnd, _ = netip.ParseAddr(f[2])
			}
		case "push":
			if strings.HasPrefix(f[1], "redirect-gateway") {
				s.PushesFullTunnel = true
			}
		}
	}
	return s
}

// InPool reports whether a is inside an explicit ifconfig-pool range.
func (s Server) InPool(a netip.Addr) bool {
	return s.PoolStart.IsValid() && s.PoolEnd.IsValid() && a.Compare(s.PoolStart) >= 0 && a.Compare(s.PoolEnd) <= 0
}
