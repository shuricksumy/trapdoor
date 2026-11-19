package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
)

// Client calls the agent API. It is used by the web process.
type Client struct {
	base   string
	token  string
	server string // set by Server; required by the per-server calls
	http   *http.Client
	slow   *http.Client // for restarts, which wait for OpenVPN
}

// NewClient returns a client for url ("http://host:port" or "unix:/path").
func NewClient(url, token string, timeout time.Duration) *Client {
	c := &Client{
		base:  strings.TrimRight(url, "/"),
		token: token,
		http:  &http.Client{Timeout: timeout},
	}
	if path, ok := strings.CutPrefix(url, "unix:"); ok {
		c.base = "http://agent"
		c.http.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}
	}
	c.slow = &http.Client{Transport: c.http.Transport, Timeout: 3 * time.Minute}
	return c
}

// NewInsecureHealthClient is for health checks of a local TLS agent only:
// it does not check the certificate and sends no token.
func NewInsecureHealthClient(url string, timeout time.Duration) *Client {
	c := NewClient(url, "", timeout)
	c.http.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} // #nosec G402 -- /healthz carries no secrets
	return c
}

// NewPinnedClient returns a client for an agent on another host
// (https://host:port) whose TLS certificate has the given fingerprint.
func NewPinnedClient(url, token, fingerprint string, timeout time.Duration) *Client {
	c := NewClient(url, token, timeout)
	c.http.Transport = &http.Transport{TLSClientConfig: pinnedSelfSignedTLS(fingerprint), ForceAttemptHTTP2: true}
	c.slow.Transport = c.http.Transport
	return c
}

// Server returns a client for the server with the given id; the
// certificate, session and configuration calls need one.
func (c *Client) Server(id string) *Client {
	cp := *c
	cp.server = id
	return &cp
}

// path returns the URL path of a per-server endpoint.
func (c *Client) path(p string) string {
	return "/v1/servers/" + url.PathEscape(c.server) + p
}

// ServerInfo describes the client's server.
func (c *Client) ServerInfo(ctx context.Context) (api.ServerInfo, error) {
	var out api.ServerInfo
	err := c.doJSON(ctx, http.MethodGet, c.path("/info"), nil, &out)
	return out, err
}

// APIError is a non-2xx answer from the agent.
type APIError struct {
	Status  int    // HTTP status code
	Message string // agent's error message
}

func (e *APIError) Error() string { return fmt.Sprintf("agent: %s (%d)", e.Message, e.Status) }

// IsStatus reports whether err is an APIError with the given status code.
func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

// ListCerts returns all certificate entries.
func (c *Client) ListCerts(ctx context.Context) ([]api.Cert, error) {
	var out []api.Cert
	err := c.doJSON(ctx, http.MethodGet, c.path("/certs"), nil, &out)
	return out, err
}

// GetCert returns the current certificate for name.
func (c *Client) GetCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodGet, c.path("/certs/"+url.PathEscape(name)), nil, &out)
	return out, err
}

// IssueCert issues a new certificate.
func (c *Client) IssueCert(ctx context.Context, req api.IssueCertRequest) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, c.path("/certs"), req, &out)
	return out, err
}

// RevokeCert revokes the current certificate for name.
func (c *Client) RevokeCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, c.path("/certs/"+url.PathEscape(name)+"/revoke"), nil, &out)
	return out, err
}

// UnrevokeCert restores a revoked certificate.
func (c *Client) UnrevokeCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, c.path("/certs/"+url.PathEscape(name)+"/unrevoke"), nil, &out)
	return out, err
}

// RenewCert issues a new certificate with the same key.
func (c *Client) RenewCert(ctx context.Context, name string, validityDays int) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, c.path("/certs/"+url.PathEscape(name)+"/renew"),
		api.RenewCertRequest{ValidityDays: validityDays}, &out)
	return out, err
}

// RemoveCert revokes (if needed) and deletes the key material for name.
func (c *Client) RemoveCert(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, c.path("/certs/"+url.PathEscape(name)), nil, nil)
}

// Profile returns the .ovpn client profile for name.
func (c *Client) Profile(ctx context.Context, name string) ([]byte, error) {
	var out []byte
	err := c.doJSON(ctx, http.MethodGet, c.path("/certs/"+url.PathEscape(name)+"/ovpn"), nil, &out)
	return out, err
}

// Sessions returns the connected clients and server counters.
func (c *Client) Sessions(ctx context.Context) (api.Sessions, error) {
	var out api.Sessions
	err := c.doJSON(ctx, http.MethodGet, c.path("/sessions"), nil, &out)
	return out, err
}

// KillSession disconnects one session by client ID.
func (c *Client) KillSession(ctx context.Context, clientID int64) error {
	return c.doJSON(ctx, http.MethodDelete, c.path("/sessions/"+strconv.FormatInt(clientID, 10)), nil, nil)
}

// KillName disconnects all sessions of a certificate name.
func (c *Client) KillName(ctx context.Context, name string) (int, error) {
	var out api.Killed
	err := c.doJSON(ctx, http.MethodDelete, c.path("/certs/"+url.PathEscape(name)+"/sessions"), nil, &out)
	return out.Killed, err
}

// ClientAuth returns a client's second-factor status.
func (c *Client) ClientAuth(ctx context.Context, name string) (api.ClientAuth, error) {
	var out api.ClientAuth
	err := c.doJSON(ctx, http.MethodGet, c.path("/certs/"+url.PathEscape(name)+"/auth"), nil, &out)
	return out, err
}

// UpdateClientAuth changes a client's second factor.
func (c *Client) UpdateClientAuth(ctx context.Context, name string, req api.ClientAuthUpdate) (api.ClientAuth, error) {
	var out api.ClientAuth
	err := c.doJSON(ctx, http.MethodPut, c.path("/certs/"+url.PathEscape(name)+"/auth"), req, &out)
	return out, err
}

// ServerNetwork returns the VPN network from server.conf.
func (c *Client) ServerNetwork(ctx context.Context) (api.ServerNetwork, error) {
	var out api.ServerNetwork
	err := c.doJSON(ctx, http.MethodGet, c.path("/network"), nil, &out)
	return out, err
}

// ClientNetwork returns a client's network settings.
func (c *Client) ClientNetwork(ctx context.Context, name string) (api.ClientNetwork, error) {
	var out api.ClientNetwork
	err := c.doJSON(ctx, http.MethodGet, c.path("/certs/"+url.PathEscape(name)+"/network"), nil, &out)
	return out, err
}

// SetClientNetwork replaces a client's network settings.
func (c *Client) SetClientNetwork(ctx context.Context, name string, n api.ClientNetwork) (api.ClientNetwork, error) {
	var out api.ClientNetwork
	err := c.doJSON(ctx, http.MethodPut, c.path("/certs/"+url.PathEscape(name)+"/network"), n, &out)
	return out, err
}

// NamedNetworks lists the named networks.
func (c *Client) NamedNetworks(ctx context.Context) (api.NamedNetworks, error) {
	var out api.NamedNetworks
	err := c.doJSON(ctx, http.MethodGet, c.path("/named-networks"), nil, &out)
	return out, err
}

// SetNamedNetwork creates the named network, or replaces the one called
// name (n.Name may rename it).
func (c *Client) SetNamedNetwork(ctx context.Context, name string, n api.NamedNetwork) (api.NamedNetworks, error) {
	var out api.NamedNetworks
	err := c.doJSON(ctx, http.MethodPut, c.path("/named-networks/"+url.PathEscape(name)), n, &out)
	return out, err
}

// RemoveNamedNetwork removes a named network and its routes from its clients.
func (c *Client) RemoveNamedNetwork(ctx context.Context, name string) (api.NamedNetworks, error) {
	var out api.NamedNetworks
	err := c.doJSON(ctx, http.MethodDelete, c.path("/named-networks/"+url.PathEscape(name)), nil, &out)
	return out, err
}

// ServerConfig returns server.conf as text and settings.
func (c *Client) ServerConfig(ctx context.Context) (api.ServerConfig, error) {
	var out api.ServerConfig
	err := c.doJSON(ctx, http.MethodGet, c.path("/server/config"), nil, &out)
	return out, err
}

// CheckServerConfig checks a new config without saving it.
func (c *Client) CheckServerConfig(ctx context.Context, u api.ServerConfigUpdate) (api.ConfigCheck, error) {
	var out api.ConfigCheck
	err := c.doJSON(ctx, http.MethodPost, c.path("/server/config/check"), u, &out)
	return out, err
}

// SaveServerConfig saves a new config (does not restart OpenVPN).
func (c *Client) SaveServerConfig(ctx context.Context, u api.ServerConfigUpdate) (api.ServerConfig, error) {
	var out api.ServerConfig
	err := c.doJSON(ctx, http.MethodPut, c.path("/server/config"), u, &out)
	return out, err
}

// RestartServer restarts OpenVPN and waits for it.
func (c *Client) RestartServer(ctx context.Context, req api.RestartRequest) (api.RestartResult, error) {
	var out api.RestartResult
	err := c.do(ctx, c.slow, http.MethodPost, c.path("/server/restart"), req, &out)
	return out, err
}

// SetupStatus tells whether the server still needs the setup wizard.
func (c *Client) SetupStatus(ctx context.Context) (api.SetupStatus, error) {
	var out api.SetupStatus
	err := c.doJSON(ctx, http.MethodGet, c.path("/setup"), nil, &out)
	return out, err
}

// Setup creates the server configuration and waits for OpenVPN.
func (c *Client) Setup(ctx context.Context, req api.SetupRequest) (api.SetupResult, error) {
	var out api.SetupResult
	err := c.do(ctx, c.slow, http.MethodPost, c.path("/setup"), req, &out)
	return out, err
}

// Firewall returns the access rules and whether they are active.
func (c *Client) Firewall(ctx context.Context) (api.Firewall, error) {
	var out api.Firewall
	err := c.doJSON(ctx, http.MethodGet, c.path("/firewall"), nil, &out)
	return out, err
}

// SetFirewall saves and applies the access rules.
func (c *Client) SetFirewall(ctx context.Context, p api.FirewallPolicy) (api.Firewall, error) {
	var out api.Firewall
	err := c.doJSON(ctx, http.MethodPut, c.path("/firewall"), p, &out)
	return out, err
}

// AssignAddresses gives clients fixed VPN addresses (for access rules).
func (c *Client) AssignAddresses(ctx context.Context, names []string) (api.Firewall, error) {
	var out api.Firewall
	err := c.doJSON(ctx, http.MethodPost, c.path("/firewall/addresses"), api.AssignAddresses{Names: names}, &out)
	return out, err
}

// ConfigBackups lists saved earlier server.conf versions.
func (c *Client) ConfigBackups(ctx context.Context) ([]api.ConfigBackup, error) {
	var out []api.ConfigBackup
	err := c.doJSON(ctx, http.MethodGet, c.path("/server/backups"), nil, &out)
	return out, err
}

// ConfigBackup returns one backup's text.
func (c *Client) ConfigBackup(ctx context.Context, name string) (string, error) {
	var out struct {
		Raw string `json:"raw"`
	}
	err := c.doJSON(ctx, http.MethodGet, c.path("/server/backups/"+url.PathEscape(name)), nil, &out)
	return out.Raw, err
}

// GenCRL regenerates the CRL.
func (c *Client) GenCRL(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, c.path("/crl"), nil, nil)
}

// Health checks GET /healthz (no token needed); it works for agent and web.
func (c *Client) Health(ctx context.Context) error {
	var h api.Health
	if err := c.doJSON(ctx, http.MethodGet, "/healthz", nil, &h); err != nil {
		return err
	}
	if h.Status != "ok" {
		return fmt.Errorf("health: %q", h.Status)
	}
	return nil
}

// Info returns the agent host and its servers.
func (c *Client) Info(ctx context.Context) (api.AgentInfo, error) {
	var out api.AgentInfo
	err := c.doJSON(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, err
}

// StartServer starts the client's server (stopped in trapdoor before).
func (c *Client) StartServer(ctx context.Context) (api.ServerInfo, error) {
	var out api.ServerInfo
	err := c.do(ctx, c.slow, http.MethodPost, c.path("/start"), nil, &out)
	return out, err
}

// StopServer stops the client's server until it is started again.
func (c *Client) StopServer(ctx context.Context) (api.ServerInfo, error) {
	var out api.ServerInfo
	err := c.doJSON(ctx, http.MethodPost, c.path("/stop"), nil, &out)
	return out, err
}

// SetAutostart sets whether the server starts with the VPN container.
func (c *Client) SetAutostart(ctx context.Context, on bool) (api.ServerInfo, error) {
	var out api.ServerInfo
	err := c.doJSON(ctx, http.MethodPut, c.path("/autostart"), api.Autostart{Autostart: on}, &out)
	return out, err
}

// Backup streams the agent's directories as a tar (see Host.handleBackup).
// The caller closes it; a cut stream shows up as a tar error.
func (c *Client) Backup(ctx context.Context) (io.ReadCloser, error) {
	return c.stream(ctx, http.MethodGet, "/v1/backup", nil, "")
}

// ServerBackup streams the client's server as a tar (server.json first).
func (c *Client) ServerBackup(ctx context.Context) (io.ReadCloser, error) {
	return c.stream(ctx, http.MethodGet, c.path("/backup"), nil, "")
}

// RestoreServer brings a server back from the tar of its backup; replace
// removes a server with the same id first.
func (c *Client) RestoreServer(ctx context.Context, tarball io.Reader, replace bool) (api.ServerInfo, error) {
	path := "/v1/servers/restore"
	if replace {
		path += "?replace=1"
	}
	body, err := c.stream(ctx, http.MethodPost, path, tarball, "application/x-tar")
	if err != nil {
		return api.ServerInfo{}, err
	}
	defer func() { _ = body.Close() }()
	var out api.ServerInfo
	err = json.NewDecoder(body).Decode(&out)
	return out, err
}

// stream is a request whose body (in or out) can be large: no overall
// timeout, the context bounds it.
func (c *Client) stream(ctx context.Context, method, path string, in io.Reader, contentType string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, in)
	if err != nil {
		return nil, err
	}
	req.Header.Set(api.HeaderToken, c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	hc := &http.Client{Transport: c.http.Transport}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 != 2 {
		defer func() { _ = resp.Body.Close() }()
		var e api.Error
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e)
		return nil, &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	return resp.Body, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	return c.do(ctx, c.http, method, path, in, out)
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(api.HeaderToken, c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		var e api.Error
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// CreateServer creates a WireGuard server on the agent host.
func (c *Client) CreateServer(ctx context.Context, req api.CreateServer) (api.ServerInfo, error) {
	var out api.ServerInfo
	err := c.doJSON(ctx, http.MethodPost, "/v1/servers", req, &out)
	return out, err
}

// RemoveServer removes the client's (WireGuard) server and its interface.
func (c *Client) RemoveServer(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/servers/"+url.PathEscape(c.server), nil, nil)
}

// AddPeers adds several WireGuard clients at once.
func (c *Client) AddPeers(ctx context.Context, req api.NewPeers) ([]api.Peer, error) {
	var out []api.Peer
	err := c.doJSON(ctx, http.MethodPost, c.path("/peers/bulk"), req, &out)
	return out, err
}

// WGSettings returns a WireGuard server's settings and client defaults.
func (c *Client) WGSettings(ctx context.Context) (api.WGSettings, error) {
	var out api.WGSettings
	err := c.doJSON(ctx, http.MethodGet, c.path("/settings"), nil, &out)
	return out, err
}

// SetWGSettings changes them.
func (c *Client) SetWGSettings(ctx context.Context, set api.WGSettings) (api.WGSettings, error) {
	var out api.WGSettings
	err := c.doJSON(ctx, http.MethodPut, c.path("/settings"), set, &out)
	return out, err
}

// Peers lists the WireGuard clients of the server.
func (c *Client) Peers(ctx context.Context) ([]api.Peer, error) {
	var out []api.Peer
	err := c.doJSON(ctx, http.MethodGet, c.path("/peers"), nil, &out)
	return out, err
}

// Peer returns one WireGuard client.
func (c *Client) Peer(ctx context.Context, name string) (api.Peer, error) {
	var out api.Peer
	err := c.doJSON(ctx, http.MethodGet, c.path("/peers/"+url.PathEscape(name)), nil, &out)
	return out, err
}

// AddPeer adds a WireGuard client with new keys.
func (c *Client) AddPeer(ctx context.Context, req api.NewPeer) (api.Peer, error) {
	var out api.Peer
	err := c.doJSON(ctx, http.MethodPost, c.path("/peers"), req, &out)
	return out, err
}

// UpdatePeer enables / disables a WireGuard client or changes its LANs.
func (c *Client) UpdatePeer(ctx context.Context, name string, u api.PeerUpdate) (api.Peer, error) {
	var out api.Peer
	err := c.doJSON(ctx, http.MethodPut, c.path("/peers/"+url.PathEscape(name)), u, &out)
	return out, err
}

// RemovePeer deletes a WireGuard client; it cannot connect any more.
func (c *Client) RemovePeer(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, c.path("/peers/"+url.PathEscape(name)), nil, nil)
}

// PeerConf returns a WireGuard client's configuration file.
func (c *Client) PeerConf(ctx context.Context, name string) ([]byte, error) {
	var out []byte
	err := c.doJSON(ctx, http.MethodGet, c.path("/peers/"+url.PathEscape(name)+"/conf"), nil, &out)
	return out, err
}
