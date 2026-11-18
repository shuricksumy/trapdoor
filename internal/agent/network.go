package agent

import (
	"errors"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/ccdnet"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/pki"
)

func (s *Server) registerNetworkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/network", s.handleServerNetwork)
	mux.HandleFunc("GET /v1/certs/{name}/network", s.handleGetClientNetwork)
	mux.HandleFunc("PUT /v1/certs/{name}/network", s.handleSetClientNetwork)
}

func (s *Server) serverConf() (ccdnet.Server, error) {
	data, err := os.ReadFile(filepath.Join(s.cfg.OpenVPNDir, "server.conf"))
	if errors.Is(err, fs.ErrNotExist) {
		return ccdnet.Server{Topology: ccdnet.TopologyNet30}, nil
	}
	if err != nil {
		return ccdnet.Server{}, err
	}
	return ccdnet.ParseServer(string(data)), nil
}

// staticIPs returns the static address of every client with one.
func (s *Server) staticIPs() (map[string]netip.Addr, error) {
	files, err := s.ccd.All()
	if err != nil {
		return nil, err
	}
	out := map[string]netip.Addr{}
	for name, data := range files {
		if n, err := ccdnet.Parse(data); err == nil && n.StaticIP.IsValid() {
			out[name] = n.StaticIP
		}
	}
	return out, nil
}

func (s *Server) handleServerNetwork(w http.ResponseWriter, _ *http.Request) {
	srv, err := s.serverConf()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	files, err := s.ccd.All()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ips := map[string]netip.Addr{}
	lans := map[string][]string{}
	for name, data := range files {
		n, err := ccdnet.Parse(data)
		if err != nil {
			continue
		}
		if n.StaticIP.IsValid() {
			ips[name] = n.StaticIP
		}
		if len(n.LAN) > 0 {
			lans[name] = prefixes(n.LAN)
		}
	}
	out := api.ServerNetwork{
		ClientLANs:       lans,
		Topology:         string(srv.Topology),
		ClientConfigDir:  srv.ClientConfigDir,
		PushesFullTunnel: srv.PushesFullTunnel,
		Routes:           prefixes(srv.Routes),
		StaticIPs:        map[string]string{},
	}
	if srv.Subnet.IsValid() {
		out.Subnet = srv.Subnet.String()
		out.ServerIP = srv.ServerIP().String()
	}
	for n, a := range ips {
		out.StaticIPs[n] = a.String()
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleGetClientNetwork(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	data, err := s.ccd.Read(name)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	n, err := ccdnet.Parse(data)
	if err != nil {
		httpx.WriteError(w, http.StatusUnprocessableEntity, "ccd/"+name+": "+err.Error())
		return
	}
	out := toClientNetwork(n)
	if srv, err := s.serverConf(); err == nil {
		ips, _ := s.staticIPs()
		out.Warnings, _ = ccdnet.Validate(name, n, srv, ips)
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetClientNetwork(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !pki.ValidName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "invalid name")
		return
	}
	var req api.ClientNetwork
	if !decodeBody(w, r, &req) {
		return
	}
	if s.PKI() != nil {
		if _, err := s.PKI().Get(r.Context(), name); err != nil {
			s.pkiError(w, err)
			return
		}
	}
	n, err := fromClientNetwork(req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	srv, err := s.serverConf()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ips, err := s.staticIPs()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.netMu.Lock()
	defer s.netMu.Unlock()
	warnings, err := ccdnet.Validate(name, n, srv, ips)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.ccd.Edit(name, func(cur string) (string, error) {
		return ccdnet.Apply(cur, n, srv), nil
	}); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("client network updated", "name", name, "static_ip", req.StaticIP, "routes", len(n.Routes), "lan", len(n.LAN))
	s.refreshFirewall(r.Context())
	out := toClientNetwork(n)
	out.Warnings = warnings
	httpx.WriteJSON(w, http.StatusOK, out)
}

func toClientNetwork(n ccdnet.Net) api.ClientNetwork {
	out := api.ClientNetwork{FullTunnel: n.FullTunnel, Routes: prefixes(n.Routes), LAN: prefixes(n.LAN)}
	if n.StaticIP.IsValid() {
		out.StaticIP = n.StaticIP.String()
	}
	return out
}

func fromClientNetwork(c api.ClientNetwork) (ccdnet.Net, error) {
	n := ccdnet.Net{FullTunnel: c.FullTunnel}
	if c.StaticIP != "" {
		a, err := netip.ParseAddr(c.StaticIP)
		if err != nil || !a.Is4() {
			return n, errors.New("static_ip must be an IPv4 address")
		}
		n.StaticIP = a
	}
	if len(c.Routes)+len(c.LAN) > 100 {
		return n, errors.New("too many networks (max 100)")
	}
	seen := map[netip.Prefix]bool{}
	parse := func(list []string) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, s := range list {
			p, err := ccdnet.ParsePrefix(s)
			if err != nil {
				return nil, err
			}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Addr().Less(out[j].Addr()) })
		return out, nil
	}
	var err error
	if n.Routes, err = parse(c.Routes); err != nil {
		return n, err
	}
	seen = map[netip.Prefix]bool{}
	if n.LAN, err = parse(c.LAN); err != nil {
		return n, err
	}
	return n, nil
}

func prefixes(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}
