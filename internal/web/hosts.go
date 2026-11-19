package web

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// Several hosts: the web process talks to its own agent (TD_AGENT_URL) and
// to agents on other hosts, added in the UI. Servers on another host get
// the id "<host>.<id>" in the web API and the history database; the own
// agent's servers keep their ids (existing history stays).

// hostPattern: host names; no dots, so "<host>.<id>" splits at the first.
var hostPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)

// hostInfoTimeout bounds asking another host for its servers, so one
// unreachable host does not slow every page down.
const hostInfoTimeout = 5 * time.Second

// HostManager lists, adds and removes other hosts (nodes).
type HostManager interface {
	Hosts(ctx context.Context) []api.HostStatus
	AddHost(ctx context.Context, h api.Host) (api.HostStatus, error)
	RemoveHost(ctx context.Context, name string) error
	// HostFingerprint is the pinned fingerprint of a node, if it exists.
	HostFingerprint(name string) (string, bool)
}

// HostFingerprint returns a node's pinned certificate fingerprint.
func (m *MultiAgent) HostFingerprint(name string) (string, bool) {
	r, ok := m.host(name)
	return r.fingerprint, ok
}

// MultiAgent is an Agent spanning the own agent and the other hosts'.
type MultiAgent struct {
	local   *agent.Client
	db      *store.DB
	timeout time.Duration
	now     func() time.Time

	mu     sync.RWMutex
	remote map[string]remoteHost
}

type remoteHost struct {
	url, fingerprint string
	c                *agent.Client
}

// NewMultiAgent loads the other hosts from db.
func NewMultiAgent(ctx context.Context, local *agent.Client, db *store.DB, timeout time.Duration) (*MultiAgent, error) {
	m := &MultiAgent{local: local, db: db, timeout: timeout, now: time.Now, remote: map[string]remoteHost{}}
	hosts, err := db.Hosts(ctx)
	if err != nil {
		return nil, err
	}
	for _, h := range hosts {
		m.remote[h.Name] = remoteHost{url: h.URL, fingerprint: h.Fingerprint, c: agent.NewPinnedClient(h.URL, h.Token, h.Fingerprint, timeout)}
	}
	return m, nil
}

func (m *MultiAgent) hostList() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.remote))
	for n := range m.remote {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (m *MultiAgent) host(name string) (remoteHost, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.remote[name]
	return r, ok
}

// remoteInfo asks every other host for its servers, in parallel.
func (m *MultiAgent) remoteInfo(ctx context.Context) ([]api.HostStatus, []api.ServerInfo) {
	names := m.hostList()
	type result struct {
		st      api.HostStatus
		servers []api.ServerInfo
	}
	res := make([]result, len(names))
	var wg sync.WaitGroup
	for i, n := range names {
		r, _ := m.host(n)
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(ctx, hostInfoTimeout)
			defer cancel()
			st := api.HostStatus{Name: n, URL: r.url}
			info, err := r.c.Info(ctx)
			if err != nil {
				st.Error = err.Error()
				res[i] = result{st: st}
				return
			}
			st.Version, st.Hostname, st.Containers, st.Machine = info.Version, info.Hostname, info.Containers, info.Machine
			for _, s := range info.Servers {
				s.ID, s.Host = n+"."+s.ID, n
				res[i].servers = append(res[i].servers, s)
			}
			res[i].st = st
		}()
	}
	wg.Wait()
	var hosts []api.HostStatus
	var servers []api.ServerInfo
	for _, r := range res {
		hosts = append(hosts, r.st)
		servers = append(servers, r.servers...)
	}
	return hosts, servers
}

// Info is the own agent's info with the other hosts' servers added.
func (m *MultiAgent) Info(ctx context.Context) (api.AgentInfo, error) {
	info, err := m.local.Info(ctx)
	if err != nil {
		return info, err
	}
	hosts, servers := m.remoteInfo(ctx)
	info.Servers = append(info.Servers, servers...)
	info.Hosts = hosts
	return info, nil
}

// CreateServer creates the server on req.Host (empty: the own agent).
func (m *MultiAgent) CreateServer(ctx context.Context, req api.CreateServer) (api.ServerInfo, error) {
	name := req.Host
	req.Host = ""
	if name == "" {
		return m.local.CreateServer(ctx, req)
	}
	r, ok := m.host(name)
	if !ok {
		return api.ServerInfo{}, &agent.APIError{Status: http.StatusNotFound, Message: "no host " + name}
	}
	info, err := r.c.CreateServer(ctx, req)
	if err == nil {
		info.ID, info.Host = name+"."+info.ID, name
	}
	return info, err
}

// Server returns the client for a server id; "<host>.<id>" goes to that
// host. An unknown host goes to the own agent, which answers 404.
func (m *MultiAgent) Server(id string) AgentClient {
	if name, sid, ok := strings.Cut(id, "."); ok {
		if r, ok := m.host(name); ok {
			return r.c.Server(sid)
		}
	}
	return m.local.Server(id)
}

// Hosts lists the other hosts and whether they answer.
func (m *MultiAgent) Hosts(ctx context.Context) []api.HostStatus {
	hosts, _ := m.remoteInfo(ctx)
	if hosts == nil {
		hosts = []api.HostStatus{}
	}
	return hosts
}

// AddHost checks that the agent answers with the pinned certificate and
// token, then saves it (replacing a host with the same name).
func (m *MultiAgent) AddHost(ctx context.Context, h api.Host) (api.HostStatus, error) {
	if !hostPattern.MatchString(h.Name) {
		return api.HostStatus{}, errors.New("name: lower-case letters, digits and dashes, starting with a letter, at most 31")
	}
	u, err := url.Parse(strings.TrimSpace(h.URL))
	if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") {
		return api.HostStatus{}, errors.New("url must be the agent's address like https://203.0.113.5:9443")
	}
	h.URL = "https://" + u.Host
	if h.Fingerprint, err = agent.NormalizeFingerprint(h.Fingerprint); err != nil {
		return api.HostStatus{}, err
	}
	if strings.TrimSpace(h.Token) == "" {
		return api.HostStatus{}, errors.New("token: the agent's TD_AGENT_TOKEN")
	}
	c := agent.NewPinnedClient(h.URL, h.Token, h.Fingerprint, m.timeout)
	cctx, cancel := context.WithTimeout(ctx, hostInfoTimeout)
	defer cancel()
	info, err := c.Info(cctx)
	if err != nil {
		return api.HostStatus{}, fmt.Errorf("the agent at %s did not answer: %w", h.URL, err)
	}
	if err := m.db.SaveHost(ctx, store.Host{Name: h.Name, URL: h.URL, Token: h.Token, Fingerprint: h.Fingerprint}, m.now().Unix()); err != nil {
		return api.HostStatus{}, err
	}
	m.mu.Lock()
	m.remote[h.Name] = remoteHost{url: h.URL, fingerprint: h.Fingerprint, c: c}
	m.mu.Unlock()
	return api.HostStatus{Name: h.Name, Machine: info.Machine, URL: h.URL, Version: info.Version, Hostname: info.Hostname, Containers: info.Containers}, nil
}

// RemoveHost forgets a host; its servers keep running there and their
// history stays.
func (m *MultiAgent) RemoveHost(ctx context.Context, name string) error {
	ok, err := m.db.RemoveHost(ctx, name)
	if err != nil {
		return err
	}
	if !ok {
		return &agent.APIError{Status: http.StatusNotFound, Message: "no host " + name}
	}
	m.mu.Lock()
	delete(m.remote, name)
	m.mu.Unlock()
	return nil
}

func (s *Server) registerHostRoutes(mux *http.ServeMux) {
	// Nodes were called hosts first; both paths work.
	for _, base := range []string{"/api/v1/hosts", "/api/v1/nodes"} {
		mux.HandleFunc("GET "+base, s.handleHosts)
		mux.HandleFunc("POST "+base, s.handleAddHost)
		mux.HandleFunc("DELETE "+base+"/{name}", s.handleRemoveHost)
	}
	mux.HandleFunc("POST /api/v1/nodes/tokens", s.handleJoinToken)
}

func (s *Server) handleHosts(w http.ResponseWriter, r *http.Request) {
	if s.Hosts == nil {
		httpx.WriteJSON(w, http.StatusOK, []api.HostStatus{})
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.Hosts.Hosts(r.Context()))
}

func (s *Server) handleAddHost(w http.ResponseWriter, r *http.Request) {
	if s.Hosts == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "hosts are not available")
		return
	}
	var req api.Host
	if !decodeJSON(w, r, &req) {
		return
	}
	st, err := s.Hosts.AddHost(r.Context(), req)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit(r, "host.add", st.Name)
	httpx.WriteJSON(w, http.StatusCreated, st)
}

func (s *Server) handleRemoveHost(w http.ResponseWriter, r *http.Request) {
	if s.Hosts == nil {
		httpx.WriteError(w, http.StatusNotFound, "no host "+r.PathValue("name"))
		return
	}
	if err := s.Hosts.RemoveHost(r.Context(), r.PathValue("name")); err != nil {
		s.agentError(w, err)
		return
	}
	s.audit(r, "host.remove", r.PathValue("name"))
	w.WriteHeader(http.StatusNoContent)
}
