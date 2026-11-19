package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/ccdnet"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// Named networks (openvpn-ui "routes"): a set of networks with a name,
// given to several clients. They are kept in trapdoor-networks.json; their
// routes are written into the ccd file of every client they are given to,
// next to the routes set on the client itself, so OpenVPN and the ccd
// format do not change.

func (s *Server) registerNamedRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /named-networks", s.handleNamedNetworks)
	mux.HandleFunc("PUT /named-networks/{name}", s.handleSetNamedNetwork)
	mux.HandleFunc("DELETE /named-networks/{name}", s.handleRemoveNamedNetwork)
}

var namedPattern = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} ._-]{0,39}$`)

func (s *Server) namedPath() string { return filepath.Join(s.cfg.OpenVPNDir, "trapdoor-networks.json") }

// loadNamed reads the named networks (none when the file does not exist).
func (s *Server) loadNamed() ([]api.NamedNetwork, error) {
	data, err := os.ReadFile(s.namedPath())
	if errors.Is(err, fs.ErrNotExist) {
		return []api.NamedNetwork{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []api.NamedNetwork
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("%s: %w", s.namedPath(), err)
	}
	return out, nil
}

// namedFor are the routes and networks behind the client that come from
// named networks.
func namedFor(nets []api.NamedNetwork, client string) (routes, lan []netip.Prefix, uses []api.NamedUse) {
	for _, n := range nets {
		var dst *[]netip.Prefix
		switch {
		case n.Via == client:
			dst = &lan
			uses = append(uses, api.NamedUse{Name: n.Name, As: api.NamedLAN})
		case slices.Contains(n.Clients, client):
			dst = &routes
			uses = append(uses, api.NamedUse{Name: n.Name, As: api.NamedRoute})
		default:
			continue
		}
		for _, c := range n.Networks {
			if p, err := ccdnet.ParsePrefix(c); err == nil {
				*dst = append(*dst, p)
			}
		}
	}
	return routes, lan, uses
}

// without returns ps minus drop.
func without(ps, drop []netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ps {
		if !slices.Contains(drop, p) {
			out = append(out, p)
		}
	}
	return out
}

// union returns a ∪ b, sorted, without duplicates.
func union(a, b []netip.Prefix) []netip.Prefix {
	out := slices.Clone(a)
	for _, p := range b {
		if !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Addr() == out[j].Addr() {
			return out[i].Bits() < out[j].Bits()
		}
		return out[i].Addr().Less(out[j].Addr())
	})
	return out
}

// ownNet is the part of a client's ccd settings set on the client itself:
// the ccd file minus what the named networks add.
func ownNet(n ccdnet.Net, nets []api.NamedNetwork, client string) ccdnet.Net {
	r, l, _ := namedFor(nets, client)
	n.Routes, n.LAN = without(n.Routes, r), without(n.LAN, l)
	return n
}

// withNamed adds the named networks' routes to the client's own settings.
func withNamed(own ccdnet.Net, nets []api.NamedNetwork, client string) ccdnet.Net {
	r, l, _ := namedFor(nets, client)
	own.Routes, own.LAN = union(own.Routes, r), union(own.LAN, l)
	return own
}

// normalizeNamed checks a named network from the API and puts it in a
// canonical form.
func normalizeNamed(n *api.NamedNetwork) error {
	n.Name = strings.TrimSpace(n.Name)
	n.Description = strings.TrimSpace(n.Description)
	if !namedPattern.MatchString(n.Name) {
		return errors.New("name: letters, digits, spaces, dots, dashes and underscores, at most 40")
	}
	if len(n.Description) > 200 {
		return errors.New("description: at most 200 characters")
	}
	if len(n.Networks) == 0 || len(n.Networks) > 50 {
		return errors.New("give 1-50 networks")
	}
	var ps []netip.Prefix
	for _, c := range n.Networks {
		p, err := ccdnet.ParsePrefix(c)
		if err != nil {
			return err
		}
		if !p.Addr().Is4() {
			return fmt.Errorf("%s: only IPv4 networks are supported", p)
		}
		ps = union(ps, []netip.Prefix{p})
	}
	n.Networks = prefixes(ps)
	if n.Via != "" && !pki.ValidName(n.Via) {
		return fmt.Errorf("via: %q is not a client name", n.Via)
	}
	clients := []string{}
	for _, c := range n.Clients {
		if !pki.ValidName(c) {
			return fmt.Errorf("clients: %q is not a client name", c)
		}
		if c != n.Via && !slices.Contains(clients, c) {
			clients = append(clients, c)
		}
	}
	sort.Strings(clients)
	n.Clients = clients
	return nil
}

func (s *Server) handleNamedNetworks(w http.ResponseWriter, _ *http.Request) {
	nets, err := s.loadNamed()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.NamedNetworks{Networks: nets, Warnings: []string{}})
}

func (s *Server) handleSetNamedNetwork(w http.ResponseWriter, r *http.Request) {
	var req api.NamedNetwork
	if !decodeBody(w, r, &req) {
		return
	}
	old := r.PathValue("name")
	if req.Name == "" {
		req.Name = old
	}
	if err := normalizeNamed(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Only clients that exist (a renamed or deleted one would get a ccd file).
	if p := s.PKI(); p != nil {
		for _, c := range append(slices.Clone(req.Clients), req.Via) {
			if c == "" {
				continue
			}
			if _, err := p.Get(r.Context(), c); err != nil {
				s.pkiError(w, err)
				return
			}
		}
	}
	s.changeNamed(w, r, func(nets []api.NamedNetwork) ([]api.NamedNetwork, int, error) {
		out := []api.NamedNetwork{}
		found := false
		for _, n := range nets {
			switch {
			case n.Name == old:
				out = append(out, req)
				found = true
			case n.Name == req.Name:
				return nil, http.StatusConflict, fmt.Errorf("a network named %q exists", req.Name)
			default:
				out = append(out, n)
			}
		}
		if !found {
			out = append(out, req)
		}
		return out, 0, nil
	}, "named network saved", req.Name)
}

func (s *Server) handleRemoveNamedNetwork(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.changeNamed(w, r, func(nets []api.NamedNetwork) ([]api.NamedNetwork, int, error) {
		out := []api.NamedNetwork{}
		for _, n := range nets {
			if n.Name != name {
				out = append(out, n)
			}
		}
		if len(out) == len(nets) {
			return nil, http.StatusNotFound, fmt.Errorf("no network named %q", name)
		}
		return out, 0, nil
	}, "named network removed", name)
}

// changeNamed applies change to the named networks and rewrites the ccd
// files of every client whose routes change. Every file is checked first:
// nothing is written when one of them would be invalid.
func (s *Server) changeNamed(w http.ResponseWriter, r *http.Request,
	change func([]api.NamedNetwork) ([]api.NamedNetwork, int, error), msg, name string) {
	s.netMu.Lock()
	defer s.netMu.Unlock()
	old, err := s.loadNamed()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	next, status, err := change(old)
	if err != nil {
		httpx.WriteError(w, status, err.Error())
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
	// Clients in the old or the new version of any network.
	var clients []string
	for _, set := range [][]api.NamedNetwork{old, next} {
		for _, n := range set {
			for _, c := range append(slices.Clone(n.Clients), n.Via) {
				if c != "" && !slices.Contains(clients, c) {
					clients = append(clients, c)
				}
			}
		}
	}
	sort.Strings(clients)
	type update struct {
		name string
		net  ccdnet.Net
	}
	var updates []update
	warnings := []string{}
	for _, c := range clients {
		if p := s.PKI(); p != nil {
			if _, err := p.Get(r.Context(), c); errors.Is(err, pki.ErrNotFound) {
				continue // deleted since; its name stays in the network until edited
			}
		}
		data, err := s.ccd.Read(c)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		cur, err := ccdnet.Parse(data)
		if err != nil {
			httpx.WriteError(w, http.StatusUnprocessableEntity, "ccd/"+c+": "+err.Error())
			return
		}
		n := withNamed(ownNet(cur, old, c), next, c)
		if slices.Equal(n.Routes, cur.Routes) && slices.Equal(n.LAN, cur.LAN) {
			continue
		}
		ws, err := ccdnet.Validate(c, n, srv, ips)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, c+": "+err.Error())
			return
		}
		for _, x := range ws {
			if !slices.Contains(warnings, x) {
				warnings = append(warnings, x)
			}
		}
		updates = append(updates, update{c, n})
	}
	data, _ := json.MarshalIndent(next, "", "  ")
	if err := writeAtomic(s.namedPath(), append(data, '\n'), 0o644); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, u := range updates {
		if err := s.ccd.Edit(u.name, func(cur string) (string, error) {
			return ccdnet.Apply(cur, u.net, srv), nil
		}); err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, u.name+": "+err.Error())
			return
		}
	}
	s.log.Info(msg, "network", name, "clients updated", len(updates))
	s.refreshFirewall(r.Context())
	httpx.WriteJSON(w, http.StatusOK, api.NamedNetworks{Networks: next, Warnings: warnings})
}
