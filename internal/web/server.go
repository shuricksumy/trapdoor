// Package web implements the unprivileged web/API process.
package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/version"
	"github.com/shuricksumy/trapdoor/internal/webui"
)

// Agent is the agent host: its servers, and a client for each.
// Defined here so tests can supply a fake.
type Agent interface {
	Info(ctx context.Context) (api.AgentInfo, error)
	CreateServer(ctx context.Context, req api.CreateServer) (api.ServerInfo, error)
	Server(id string) AgentClient
}

// ClientAgent adapts *agent.Client to Agent.
func ClientAgent(c *agent.Client) Agent { return clientAgent{c} }

type clientAgent struct{ *agent.Client }

func (a clientAgent) Server(id string) AgentClient { return a.Client.Server(id) }

// AgentClient is the per-server part of the agent API the web process uses.
type AgentClient interface {
	ServerInfo(ctx context.Context) (api.ServerInfo, error)
	Sessions(ctx context.Context) (api.Sessions, error)
	ListCerts(ctx context.Context) ([]api.Cert, error)
	GetCert(ctx context.Context, name string) (api.Cert, error)
	IssueCert(ctx context.Context, req api.IssueCertRequest) (api.Cert, error)
	RevokeCert(ctx context.Context, name string) (api.Cert, error)
	UnrevokeCert(ctx context.Context, name string) (api.Cert, error)
	RenewCert(ctx context.Context, name string, validityDays int) (api.Cert, error)
	RemoveCert(ctx context.Context, name string) error
	Profile(ctx context.Context, name string) ([]byte, error)
	KillSession(ctx context.Context, clientID int64) error
	KillName(ctx context.Context, name string) (int, error)
	ClientAuth(ctx context.Context, name string) (api.ClientAuth, error)
	UpdateClientAuth(ctx context.Context, name string, req api.ClientAuthUpdate) (api.ClientAuth, error)
	ServerNetwork(ctx context.Context) (api.ServerNetwork, error)
	ClientNetwork(ctx context.Context, name string) (api.ClientNetwork, error)
	SetClientNetwork(ctx context.Context, name string, n api.ClientNetwork) (api.ClientNetwork, error)
	ServerBackup(ctx context.Context) (io.ReadCloser, error)
	StartServer(ctx context.Context) (api.ServerInfo, error)
	StopServer(ctx context.Context) (api.ServerInfo, error)
	SetAutostart(ctx context.Context, on bool) (api.ServerInfo, error)
	NamedNetworks(ctx context.Context) (api.NamedNetworks, error)
	ClientsNetwork(ctx context.Context) ([]api.ClientRoutes, error)
	SetNamedNetwork(ctx context.Context, name string, n api.NamedNetwork) (api.NamedNetworks, error)
	RemoveNamedNetwork(ctx context.Context, name string) (api.NamedNetworks, error)
	ServerConfig(ctx context.Context) (api.ServerConfig, error)
	CheckServerConfig(ctx context.Context, u api.ServerConfigUpdate) (api.ConfigCheck, error)
	SaveServerConfig(ctx context.Context, u api.ServerConfigUpdate) (api.ServerConfig, error)
	RestartServer(ctx context.Context, req api.RestartRequest) (api.RestartResult, error)
	ConfigBackups(ctx context.Context) ([]api.ConfigBackup, error)
	ConfigBackup(ctx context.Context, name string) (string, error)
	SetupStatus(ctx context.Context) (api.SetupStatus, error)
	Setup(ctx context.Context, req api.SetupRequest) (api.SetupResult, error)
	Firewall(ctx context.Context) (api.Firewall, error)
	SetFirewall(ctx context.Context, p api.FirewallPolicy) (api.Firewall, error)
	AssignAddresses(ctx context.Context, names []string) (api.Firewall, error)

	// WireGuard servers.
	RemoveServer(ctx context.Context) error
	Peers(ctx context.Context) ([]api.Peer, error)
	Peer(ctx context.Context, name string) (api.Peer, error)
	AddPeer(ctx context.Context, req api.NewPeer) (api.Peer, error)
	UpdatePeer(ctx context.Context, name string, u api.PeerUpdate) (api.Peer, error)
	RemovePeer(ctx context.Context, name string) error
	PeerConf(ctx context.Context, name string) ([]byte, error)
	AddPeers(ctx context.Context, req api.NewPeers) ([]api.Peer, error)
	WGSettings(ctx context.Context) (api.WGSettings, error)
	SetWGSettings(ctx context.Context, set api.WGSettings) (api.WGSettings, error)
}

// Deps are the web server's dependencies.
type Deps struct {
	Agent Agent
	// Hosts manages other hosts (nil: none can be added).
	Hosts HostManager
	// Backup gives the agents' parts of a backup (nil: no backups).
	Backup BackupSource
	// JoinSecret lets nodes started with it join without a one-time token
	// (TD_JOIN_SECRET; empty = only tokens).
	JoinSecret string
	// Stats is the history database; its Agent field is set per request to
	// the server in the URL.
	Stats   *history.Stats
	Auth    *auth.Service
	Limiter *auth.Limiter
	// APIToken optionally allows scripts with "Authorization: Bearer"; empty = off.
	APIToken string
	// CookieSecure marks the session cookie Secure (HTTPS only).
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (for login throttling and logs).
	TrustedProxies []netip.Prefix
	// ExpiryWarnDays flags certificates expiring within this many days.
	ExpiryWarnDays int
	Location       *time.Location
	Log            *slog.Logger
	Now            func() time.Time // nil = time.Now
}

// Server is the web HTTP server.
type Server struct {
	Deps
}

// New returns a web server.
func New(d Deps) *Server {
	if d.Location == nil {
		d.Location = time.Local
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Limiter == nil {
		d.Limiter = &auth.Limiter{}
	}
	if d.ExpiryWarnDays == 0 {
		d.ExpiryWarnDays = 30
	}
	return &Server{Deps: d}
}

// Handler returns the web process HTTP handler.
func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/status", s.handleStatus)
	api.HandleFunc("GET /api/v1/servers/{server}/sessions", s.handleLiveSessions)
	api.HandleFunc("GET /api/v1/servers/{server}/stats", s.handleStats)
	api.HandleFunc("GET /api/v1/servers/{server}/stats.csv", s.handleStatsCSV)
	api.HandleFunc("GET /api/v1/servers/{server}/history/sessions", s.handleHistorySessions)
	s.registerCertRoutes(api)
	s.registerServerRoutes(api)
	s.registerWireGuardRoutes(api)
	s.registerHostRoutes(api)
	s.registerBackupRoutes(api)
	s.registerServerControlRoutes(api)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, apiHealth())
	})
	s.registerAuthRoutes(mux, api)
	mux.HandleFunc("POST /api/v1/nodes/join", s.handleJoin) // a node, with its join token
	mux.Handle("/api/", s.requireAuth(api))
	ui := webui.Handler()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			httpx.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ui.ServeHTTP(w, r)
	})
	return httpx.LogRequests(s.Log, securityHeaders(mux))
}

// agent returns the client for the server named in the request's URL.
// An unknown id is answered by the agent with 404.
func (s *Server) agent(r *http.Request) AgentClient { return s.Agent.Server(r.PathValue("server")) }

// stats returns the history of the server named in the request's URL.
func (s *Server) stats(r *http.Request) *history.Stats {
	return &history.Stats{DB: s.Stats.DB, Agent: r.PathValue("server")}
}

func apiHealth() api.Health { return api.Health{Status: "ok"} }

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	st := api.Status{WebVersion: version.Version, ExpiryWarnDays: s.ExpiryWarnDays}
	info, err := s.Agent.Info(r.Context())
	if err != nil {
		s.Log.Warn("agent unreachable", "err", err)
		st.AgentError = err.Error()
	} else {
		st.AgentOK = true
		st.Agent = &info
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) handleLiveSessions(w http.ResponseWriter, r *http.Request) {
	live, err := s.agent(r).Sessions(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, live)
}
