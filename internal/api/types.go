// Package api contains the request/response types shared by the agent API
// and its clients (the web process). It must not import anything else from
// this module so both sides can depend on it freely.
package api

import "time"

// HeaderToken is the HTTP header carrying the shared agent token.
const HeaderToken = "X-Trapdoor-Token"

// Health is returned by GET /healthz on both processes.
type Health struct {
	Status string `json:"status"` // "ok"
}

// Server types.
const (
	TypeOpenVPN   = "openvpn"
	TypeWireGuard = "wireguard"
	TypeAmneziaWG = "amneziawg" // WireGuard with traffic obfuscation
)

// AgentInfo is returned by GET /v1/info on the agent: the host and the VPN
// servers it manages.
type AgentInfo struct {
	Version  string `json:"version"`
	Hostname string `json:"hostname"`
	// Machine is the computer this node runs on (TD_MACHINE), Node the
	// node's name (TD_NODE_NAME); both only for the UI.
	Machine string       `json:"machine,omitempty"`
	Node    string       `json:"node,omitempty"`
	Servers []ServerInfo `json:"servers"`
	// Hosts are the other hosts the web process manages (web only); their
	// servers are in Servers with ids "<host>.<id>".
	Hosts []HostStatus `json:"hosts,omitempty"`
	// Containers: where new servers of each type run on this machine
	// (TypeOpenVPN etc. → container name); only the types it can run.
	Containers map[string]string `json:"containers,omitempty"`
}

// ServerSummary is one row of GET /api/v1/servers (web): the server and
// its numbers.
type ServerSummary struct {
	ServerInfo
	Clients      int    `json:"clients"`   // usable clients (valid certificates / enabled peers)
	Connected    int    `json:"connected"` // connected now
	TodayIn      int64  `json:"today_in"`  // bytes since midnight (TD_TZ)
	TodayOut     int64  `json:"today_out"`
	SummaryError string `json:"summary_error,omitempty"` // numbers that could not be read
}

// JoinRequest is what a node sends to the web UI to add itself
// (POST /api/v1/nodes/join): a join token, how to reach it, and its
// certificate fingerprint and agent token.
type JoinRequest struct {
	Token       string `json:"token"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	AgentToken  string `json:"agent_token"`
}

// JoinToken is a one-time token for a new node (POST /api/v1/nodes/tokens);
// Token is only in the answer that creates it.
type JoinToken struct {
	Token     string    `json:"token,omitempty"`
	Name      string    `json:"name,omitempty"` // the node it is for ("" = any name)
	ExpiresAt time.Time `json:"expires_at"`
}

// Autostart is the body of PUT /v1/servers/{id}/autostart.
type Autostart struct {
	Autostart bool `json:"autostart"`
}

// Host is another machine with a trapdoor agent, added in the web UI.
type Host struct {
	Name string `json:"name"` // lower-case letters, digits, dashes
	// URL of its agent: https://203.0.113.5:9443.
	URL string `json:"url"`
	// Token is the agent's TD_AGENT_TOKEN (write only: never returned).
	Token string `json:"token,omitempty"`
	// Fingerprint of the agent's TLS certificate ("sha256:<hex>", printed
	// by `trapdoor agent-cert`): the connection is pinned to it.
	Fingerprint string `json:"fingerprint"`
}

// HostStatus is a node (another agent) and whether it answers.
type HostStatus struct {
	Name string `json:"name"`
	// Machine the node says it runs on (TD_MACHINE of its agent).
	Machine  string `json:"machine,omitempty"`
	URL      string `json:"url"`
	Version  string `json:"version,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Error    string `json:"error,omitempty"`
	// Containers as in AgentInfo, for that host.
	Containers map[string]string `json:"containers,omitempty"`
}

// ServerInfo describes one VPN server (GET /v1/servers/{id}/info).
type ServerInfo struct {
	// ID is the stable key used in URLs and in the history database.
	ID   string `json:"id"`
	Type string `json:"type"` // TypeOpenVPN
	Name string `json:"name"` // shown in the UI
	// Host is the host the server runs on; empty for the web process's own
	// agent (web only).
	Host string `json:"host,omitempty"`
	// Container the server runs in (TD_CONTAINER_*); empty = not in a container.
	Container string `json:"container,omitempty"`
	// Stopped: stopped in trapdoor (not an error). Autostart: started again
	// when the VPN container (or machine) starts.
	Stopped   bool `json:"stopped"`
	Autostart bool `json:"autostart"`
	// Note explains the result of a start or stop (e.g. what to do next).
	Note string `json:"note,omitempty"`

	OpenVPNDir string `json:"openvpn_dir,omitempty"`
	PKIDir     string `json:"pki_dir,omitempty"`
	PKIReady   bool   `json:"pki_ready"`
	// CA of the PKI (empty until it exists).
	CAName     string    `json:"ca_name,omitempty"`
	CANotAfter time.Time `json:"ca_not_after,omitzero"`
	// OpenVPNVersion is empty when the management interface is unreachable.
	OpenVPNVersion string `json:"openvpn_version,omitempty"`
	MgmtError      string `json:"mgmt_error,omitempty"`

	// WireGuard: the server's public key, port and subnet, the endpoint
	// clients use, and whether the interface is up.
	PublicKey  string `json:"public_key,omitempty"`
	ListenPort int    `json:"listen_port,omitempty"`
	Proto      string `json:"proto,omitempty"` // OpenVPN: udp or tcp
	Subnet     string `json:"subnet,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Running    bool   `json:"running,omitempty"`
	// Error is the last problem applying the configuration ("" = fine).
	Error string `json:"error,omitempty"`
	// Obfuscation of an AmneziaWG server (clients get the same values).
	Obfuscation *Obfuscation `json:"obfuscation,omitempty"`
}

// AmneziaWG obfuscation levels.
const (
	// AWGLevel15 uses the settings every AmneziaWG app and the kernel
	// module understand: junk packets, S1/S2 and single H1-H4 values.
	AWGLevel15 = "1.5"
	// AWGLevel20 adds S3/S4 and H1-H4 ranges: harder to detect, needs
	// current AmneziaWG apps.
	AWGLevel20 = "2.0"
	// AWGLevel31 adds header protection, random trailers, no cookie
	// replies and random client timings: needs AmneziaWG 3.1 apps.
	AWGLevel31 = "3.1"
)

// Obfuscation are AmneziaWG's [Interface] settings. Zero values are left
// out (AmneziaWG then behaves like plain WireGuard for that mechanism).
type Obfuscation struct {
	Level string `json:"level"` // AWGLevel15, AWGLevel20 or AWGLevel31
	// Junk packets before the handshake: count and size range.
	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	// Random bytes in front of init (S1), response (S2), cookie (S3) and
	// data (S4) packets.
	S1 int `json:"s1"`
	S2 int `json:"s2"`
	S3 int `json:"s3,omitempty"`
	S4 int `json:"s4,omitempty"`
	// Message type values: one number each (1.5) or ranges "a-b" (2.0).
	H1 string `json:"h1"`
	H2 string `json:"h2"`
	H3 string `json:"h3"`
	H4 string `json:"h4"`
	// I1-I5: optional packets imitating another protocol, sent by the
	// client before the handshake (tags like <b 0x...>, <r 16>, <t>).
	I1 string `json:"i1,omitempty"`
	I2 string `json:"i2,omitempty"`
	I3 string `json:"i3,omitempty"`
	I4 string `json:"i4,omitempty"`
	I5 string `json:"i5,omitempty"`

	// AmneziaWG 3.1 (level AWGLevel31 only).
	// HeaderProtectionKey encrypts the header fields (base64, like a
	// WireGuard key); the same on server and clients. Needs S1-S4 >= 12.
	HeaderProtectionKey string `json:"header_protection_key,omitempty"`
	// RandomTrailers appends random bytes to every packet; both sides.
	RandomTrailers bool `json:"random_trailers,omitempty"`
	// ContentPadding pads data packets by a random size from the range
	// ("a" or "a-b" bytes); both sides.
	ContentPadding string `json:"content_padding,omitempty"`
	// DisableCookies: the server sends no cookie replies, which active
	// probing can recognize (it also loses WireGuard's handshake flood
	// protection). Server only.
	DisableCookies bool `json:"disable_cookies,omitempty"`
	// Client timings in seconds ("a" or "a-b": random within the range),
	// so handshakes and keepalives do not follow WireGuard's fixed clock.
	RekeyAfterTime   string `json:"rekey_after_time,omitempty"`
	RejectAfterTime  string `json:"reject_after_time,omitempty"`
	RekeyTimeout     string `json:"rekey_timeout,omitempty"`
	KeepaliveTimeout string `json:"keepalive_timeout,omitempty"`
}

// CreateServer is the body of POST /v1/servers: a new WireGuard server.
type CreateServer struct {
	Type string `json:"type"` // TypeOpenVPN, TypeWireGuard or TypeAmneziaWG
	// Host to create it on (web only; empty = the web process's own agent).
	Host string `json:"host,omitempty"`
	// ID is also the network interface name (e.g. wg0): lower-case letters,
	// digits and dashes, at most 15 characters.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Port the server listens on (WireGuard: UDP, default 51820; OpenVPN:
	// required, Proto udp or tcp). An OpenVPN server is set up afterwards
	// with the setup wizard on this port.
	Port  int    `json:"port,omitempty"`
	Proto string `json:"proto,omitempty"`
	// PublicHost and PublicPort are what clients connect to
	// (PublicPort defaults to Port).
	PublicHost string `json:"public_host"`
	PublicPort int    `json:"public_port,omitempty"`
	// Subnet of the VPN (default 10.9.0.0/24); the server takes the first address.
	Subnet string   `json:"subnet,omitempty"`
	DNS    []string `json:"dns,omitempty"`
	MTU    int      `json:"mtu,omitempty"`
	// AllowedIPs is what clients send through the tunnel
	// (default 0.0.0.0/0, ::/0: everything).
	AllowedIPs []string `json:"allowed_ips,omitempty"`
	// Keepalive in seconds for clients behind NAT (default 25, -1 = off).
	Keepalive int `json:"keepalive,omitempty"`
	// AmneziaWG: obfuscation settings; nil generates random ones at
	// AWGLevel (default AWGLevel20). With only I1-I5 set, the rest is
	// generated.
	Obfuscation *Obfuscation `json:"obfuscation,omitempty"`
	AWGLevel    string       `json:"awg_level,omitempty"`
}

// Peer is a WireGuard client (GET /v1/servers/{id}/peers).
type Peer struct {
	Name      string    `json:"name"`
	PublicKey string    `json:"public_key"`
	Address   string    `json:"address"`       // the client's VPN address
	LAN       []string  `json:"lan,omitempty"` // networks behind the client (site-to-site)
	Disabled  bool      `json:"disabled"`      // kept, but not in the server config
	HasPSK    bool      `json:"preshared_key"` // a preshared key is set
	CreatedAt time.Time `json:"created_at"`
	// OwnKey: the client brought its own key pair; trapdoor has no private
	// key for it, so its config has a placeholder instead.
	OwnKey bool `json:"own_key"`
	// Expired: past ExpiresAt, so (like Disabled) not in the server config.
	Expired bool `json:"expired,omitempty"`
	PeerSettings
	// Live state from the interface.
	Connected     bool      `json:"connected"`
	Endpoint      string    `json:"endpoint,omitempty"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	BytesReceived int64     `json:"bytes_received"` // from the client
	BytesSent     int64     `json:"bytes_sent"`     // to the client
}

// PeerSettings are a client's own config settings; empty values use the
// server's client defaults (WGSettings).
type PeerSettings struct {
	Notes      string   `json:"notes"`
	DNS        []string `json:"dns,omitempty"`
	AllowedIPs []string `json:"allowed_ips,omitempty"` // what the client sends through the tunnel
	MTU        int      `json:"mtu,omitempty"`
	Keepalive  *int     `json:"keepalive,omitempty"` // seconds; 0 = off
	// ExpiresAt: the client stops working then (e.g. a guest); nil = never.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// NewPeer is the body of POST /v1/servers/{id}/peers.
type NewPeer struct {
	Name string `json:"name"`
	// Address is optional; the next free address of the subnet by default.
	Address string   `json:"address,omitempty"`
	LAN     []string `json:"lan,omitempty"`
	// PublicKey: the client's own key (its private key stays on the
	// device); empty = trapdoor makes a key pair.
	PublicKey string `json:"public_key,omitempty"`
	// PSK: a preshared key (a layer against future quantum attacks); nil =
	// the server's default (on unless turned off there).
	PSK *bool `json:"psk,omitempty"`
	// NoPSK is the older way to say PSK=false.
	NoPSK bool `json:"no_psk,omitempty"`
	PeerSettings
}

// NewPeers is the body of POST /v1/servers/{id}/peers/bulk: Count clients
// named <Prefix>-1, -2, … (the next free numbers) on the next free addresses.
type NewPeers struct {
	Prefix string `json:"prefix"`
	Count  int    `json:"count"`
	PSK    *bool  `json:"psk,omitempty"`
	PeerSettings
}

// WGSettings are a WireGuard / AmneziaWG server's settings that can change
// after it is created (GET / PUT /v1/servers/{id}/settings); the client
// defaults go into every client's config.
type WGSettings struct {
	Name       string   `json:"name"`
	PublicHost string   `json:"public_host"`
	PublicPort int      `json:"public_port"`
	DNS        []string `json:"dns"`
	AllowedIPs []string `json:"allowed_ips"`
	MTU        int      `json:"mtu"`       // 0 = automatic (1420 minus AmneziaWG's padding)
	Keepalive  int      `json:"keepalive"` // seconds; 0 = off
	PSK        bool     `json:"psk"`       // new clients get a preshared key
}

// HookPresets are a server's built-in extra rules, run by the vpn container
// when the server's interface is up (GET / PUT /v1/servers/{id}/scripts).
type HookPresets struct {
	// DNSRedirect sends the clients' plain DNS (port 53) to this address,
	// e.g. an AdGuard or Pi-hole; empty = off.
	DNSRedirect string `json:"dns_redirect,omitempty"`
	// BlockDoT refuses DNS-over-TLS / DNS-over-QUIC (port 853), so apps fall
	// back to plain DNS (and its redirect).
	BlockDoT bool `json:"block_dot"`
	// Isolate: clients cannot reach each other through the server.
	Isolate bool `json:"isolate"`
}

// Scripts are a server's presets and own start / stop scripts.
type Scripts struct {
	HookPresets
	// Up and Down are sh scripts run as root in the vpn container after the
	// interface is up / when it goes down, with IFACE, SUBNET, SERVER_ID,
	// TYPE and NAT_IFACE set.
	Up   string `json:"up"`
	Down string `json:"down"`
	// Editable: the agent allows the web UI to change Up and Down
	// (TD_SCRIPTS=on); otherwise they are files on the host (Files).
	Editable bool `json:"editable"`
	// Supported: a vpn container runs them (TD_FIREWALL / TD_WIREGUARD=file).
	Supported bool `json:"supported"`
	// Iface is the interface the rules apply to (tun+ for the first OpenVPN
	// server).
	Iface string `json:"iface"`
	// Files are the script files on the host (in the server's folder).
	Files []string `json:"files,omitempty"`
	// State is what the vpn container reported: "ok", "error", "inactive"
	// (the interface is down) or "pending" (not applied yet).
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// PeerUpdate is the body of PUT /v1/servers/{id}/peers/{name}.
type PeerUpdate struct {
	Disabled *bool    `json:"disabled,omitempty"`
	LAN      []string `json:"lan,omitempty"` // replaces the list when not nil
	// Address moves the client to another free address ("" = unchanged).
	Address string `json:"address,omitempty"`
	// PSK true adds a preshared key (or keeps it), false removes it.
	PSK *bool `json:"psk,omitempty"`
	// Settings replaces the client's own settings when not nil.
	Settings *PeerSettings `json:"settings,omitempty"`
}

// Status is returned by GET /api/v1/status on the web process.
type Status struct {
	WebVersion string `json:"web_version"`
	// ExpiryWarnDays: client certificates expiring within this many days
	// are flagged (TD_EXPIRY_WARN_DAYS).
	ExpiryWarnDays int        `json:"expiry_warn_days"`
	AgentOK        bool       `json:"agent_ok"`
	Agent          *AgentInfo `json:"agent,omitempty"`
	AgentError     string     `json:"agent_error,omitempty"`
}

// Error is the JSON body of every non-2xx response.
type Error struct {
	Error string `json:"error"`
}

// CertStatus values.
const (
	CertValid   = "valid"
	CertRevoked = "revoked"
	CertExpired = "expired"
)

// Cert is a certificate entry as returned by the agent.
type Cert struct {
	Name         string    `json:"name"`
	Serial       string    `json:"serial"`
	Status       string    `json:"status"`
	NotAfter     time.Time `json:"not_after"`
	RevokedAt    time.Time `json:"revoked_at,omitzero"`
	RevokeReason string    `json:"revoke_reason,omitempty"`
	Current      bool      `json:"current"`
	Kind         string    `json:"kind,omitempty"` // "client" or "server"
	// Notes is the client's readable name ("Anna's phone"), kept in its ccd
	// file; the certificate name stays the login.
	Notes string `json:"notes,omitempty"`
}

// CertNotes is the body of PUT /v1/certs/{name}/notes.
type CertNotes struct {
	Notes string `json:"notes"`
}

// IssueCertRequest is the body of POST /v1/certs.
type IssueCertRequest struct {
	Name         string `json:"name"`
	Kind         string `json:"kind,omitempty"`       // "client" (default) or "server"
	Passphrase   string `json:"passphrase,omitempty"` // encrypts the private key
	ValidityDays int    `json:"validity_days,omitempty"`
	// Notes: a readable name for the client (optional).
	Notes string `json:"notes,omitempty"`
}

// RenewCertRequest is the body of POST /v1/certs/{name}/renew.
type RenewCertRequest struct {
	ValidityDays int `json:"validity_days,omitempty"`
}

// Session is one connected VPN client.
type Session struct {
	ClientID       int64     `json:"client_id"`
	Name           string    `json:"name"`
	Username       string    `json:"username,omitempty"`
	RealAddress    string    `json:"real_address"`
	VirtualAddress string    `json:"virtual_address,omitempty"`
	VirtualIPv6    string    `json:"virtual_ipv6,omitempty"`
	BytesReceived  int64     `json:"bytes_received"`
	BytesSent      int64     `json:"bytes_sent"`
	ConnectedSince time.Time `json:"connected_since"`
	Cipher         string    `json:"cipher,omitempty"`
}

// Sessions is returned by GET /v1/sessions.
type Sessions struct {
	Time     time.Time `json:"time"`
	Clients  int64     `json:"clients"`
	BytesIn  int64     `json:"bytes_in"`
	BytesOut int64     `json:"bytes_out"`
	Sessions []Session `json:"sessions"`
}

// Killed is returned by DELETE /v1/certs/{name}/sessions.
type Killed struct {
	Killed int `json:"killed"`
}

// ClientAuth is a client's second-factor status. Secrets are never returned,
// except Secret/URI right after a new TOTP key was created.
type ClientAuth struct {
	TOTP     bool   `json:"totp"`
	TOTPUser string `json:"totp_user,omitempty"`
	Static   bool   `json:"static_password"`
	// LegacyStatic: the static password is stored in plain text (openvpn-ui);
	// setting a new one stores it hashed.
	LegacyStatic bool `json:"legacy_static,omitempty"`

	Secret string `json:"secret,omitempty"`      // base32, only when just created
	URI    string `json:"otpauth_uri,omitempty"` // for the QR code, only when just created
}

// ClientAuthUpdate is the body of PUT /v1/certs/{name}/auth.
type ClientAuthUpdate struct {
	// TOTP: "enable" (new key if none), "reset" (always a new key),
	// "disable", or empty to keep as is.
	TOTP string `json:"totp,omitempty"`
	// StaticPassword sets a new static password (stored hashed).
	StaticPassword string `json:"static_password,omitempty"`
	// RemoveStatic removes the static password.
	RemoveStatic bool `json:"remove_static,omitempty"`
}

// ClientNetwork is a client's network settings (GET/PUT /v1/certs/{name}/network).
type ClientNetwork struct {
	StaticIP   string   `json:"static_ip,omitempty"` // empty = dynamic
	FullTunnel bool     `json:"full_tunnel"`         // all traffic through the VPN
	Routes     []string `json:"routes"`              // CIDRs pushed to the client
	LAN        []string `json:"lan"`                 // CIDRs behind the client (iroute)
	Warnings   []string `json:"warnings,omitempty"`  // response only
	// Named are the named networks the client is part of (response only;
	// their networks are not in Routes / LAN, change them on the network).
	Named []NamedUse `json:"named,omitempty"`
}

// ClientRoutes is one client in GET /v1/servers/{id}/network/clients: its
// own settings, the named networks it is part of, and what is wrong.
type ClientRoutes struct {
	Name string `json:"name"`
	ClientNetwork
}

// NamedUse is a client's part in a named network.
type NamedUse struct {
	Name string `json:"name"`
	As   string `json:"as"` // NamedRoute or NamedLAN
}

// How a client is part of a named network.
const (
	NamedRoute = "route" // gets a route to the networks
	NamedLAN   = "lan"   // the networks are behind it (site-to-site)
)

// NamedNetwork is a set of networks with a name that several clients
// share (openvpn-ui "routes"). trapdoor writes its routes into the ccd
// file of every client it is given to.
type NamedNetwork struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Networks    []string `json:"networks"` // CIDRs
	// Via is the client the networks are behind (site-to-site, iroute);
	// empty when they are reached through the server (e.g. its LAN).
	Via string `json:"via,omitempty"`
	// Clients get a route to the networks.
	Clients []string `json:"clients"`
}

// NamedNetworks is the answer of GET /named-networks and of changes to it.
type NamedNetworks struct {
	Networks []NamedNetwork `json:"networks"`
	Warnings []string       `json:"warnings"`
}

// ServerNetwork is the VPN network as configured in server.conf (GET /v1/network).
type ServerNetwork struct {
	Subnet           string              `json:"subnet,omitempty"`
	ServerIP         string              `json:"server_ip,omitempty"`
	Topology         string              `json:"topology"`
	ClientConfigDir  bool                `json:"client_config_dir"`
	PushesFullTunnel bool                `json:"pushes_full_tunnel"`
	Routes           []string            `json:"routes"`      // server-side "route" lines
	StaticIPs        map[string]string   `json:"static_ips"`  // client → static address
	ClientLANs       map[string][]string `json:"client_lans"` // client → networks behind it (iroute)
}

// RenewManyRequest is the body of POST /api/v1/certs/renew.
type RenewManyRequest struct {
	Names        []string `json:"names"`
	ValidityDays int      `json:"validity_days,omitempty"`
}

// RenewResult is one entry of the bulk renew answer.
type RenewResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Cert  *Cert  `json:"cert,omitempty"`
}

// ServerSettings are the form-level server.conf options.
type ServerSettings struct {
	Port           int      `json:"port"`
	Proto          string   `json:"proto"`
	Subnet         string   `json:"subnet"`
	Topology       string   `json:"topology"`
	DNS            []string `json:"dns"`
	Domain         string   `json:"domain"`
	FullTunnel     bool     `json:"full_tunnel"`
	ClientToClient bool     `json:"client_to_client"`
	DuplicateCN    bool     `json:"duplicate_cn"`
	MaxClients     int      `json:"max_clients"`
	Routes         []string `json:"routes"`
	PushRoutes     []string `json:"push_routes"`
}

// ConfigProblem is a finding of the config check; errors block saving.
type ConfigProblem struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
}

// ServerConfig is server.conf as text and as settings (GET /v1/server/config).
type ServerConfig struct {
	Raw      string          `json:"raw"`
	Version  string          `json:"version"` // send back as base_version when saving
	Settings ServerSettings  `json:"settings"`
	Pending  bool            `json:"restart_pending"` // saved but OpenVPN not restarted yet
	Problems []ConfigProblem `json:"problems"`
	Restart  string          `json:"restart_method"` // SIGHUP or SIGTERM (supervised)
}

// ServerConfigUpdate saves (PUT) or checks (POST .../check) a new config:
// either Raw text or form Settings applied to the current file.
type ServerConfigUpdate struct {
	BaseVersion string          `json:"base_version,omitempty"`
	Raw         *string         `json:"raw,omitempty"`
	Settings    *ServerSettings `json:"settings,omitempty"`
}

// ConfigCheck is the answer of POST /v1/server/config/check.
type ConfigCheck struct {
	Raw      string          `json:"raw"` // the resulting file
	Problems []ConfigProblem `json:"problems"`
}

// RestartRequest is the body of POST /v1/server/restart.
type RestartRequest struct {
	// Rollback restores the previous config if OpenVPN does not come back.
	Rollback bool `json:"rollback"`
}

// RestartResult reports a restart.
type RestartResult struct {
	OK             bool    `json:"ok"`
	OpenVPNVersion string  `json:"openvpn_version,omitempty"`
	Seconds        float64 `json:"seconds"`
	RolledBack     bool    `json:"rolled_back,omitempty"`
	Error          string  `json:"error,omitempty"`
}

// ConfigBackup is a saved earlier server.conf.
type ConfigBackup struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// SetupRequest holds the answers of the setup wizard.
type SetupRequest struct {
	PublicHost     string   `json:"public_host"`           // what clients connect to
	PublicPort     int      `json:"public_port,omitempty"` // as seen from outside; default Port
	Port           int      `json:"port"`                  // OpenVPN listens on
	Proto          string   `json:"proto"`                 // udp or tcp
	Subnet         string   `json:"subnet"`                // e.g. 10.8.0.0/24
	DNS            []string `json:"dns"`
	FullTunnel     bool     `json:"full_tunnel"`
	ClientToClient bool     `json:"client_to_client"`
	CAName         string   `json:"ca_name"`
	KeyType        string   `json:"key_type"` // ec256, ec384, rsa2048, rsa3072, rsa4096
	TLS            string   `json:"tls"`      // tls-crypt-v2, tls-crypt, tls-auth, none
	TwoFactor      bool     `json:"two_factor"`
}

// SetupStatus tells whether the server still needs to be set up.
type SetupStatus struct {
	Needed        bool         `json:"needed"` // no server.conf yet
	HasPKI        bool         `json:"has_pki"`
	HasServerConf bool         `json:"has_server_conf"`
	CAName        string       `json:"ca_name,omitempty"` // existing CA, reused
	Defaults      SetupRequest `json:"defaults"`
	// FixedPort/FixedProto are set by the container: OpenVPN must listen
	// there because only that port is published.
	FixedPort  int    `json:"fixed_port,omitempty"`
	FixedProto string `json:"fixed_proto,omitempty"`
}

// SetupResult is the outcome of POST /v1/setup.
type SetupResult struct {
	Running        bool            `json:"running"` // OpenVPN answered on the management interface
	OpenVPNVersion string          `json:"openvpn_version,omitempty"`
	Message        string          `json:"message"`
	Warnings       []ConfigProblem `json:"warnings,omitempty"`
	ServerConf     string          `json:"server_conf"`
}

// FirewallPolicy holds the access rules for VPN clients.
type FirewallPolicy struct {
	Enabled bool           `json:"enabled"`
	Default string         `json:"default"` // allow or deny: what happens when no rule matches
	Rules   []FirewallRule `json:"rules"`
}

// FirewallRule matches traffic from a client (or all clients) to a network.
// The first matching rule decides.
type FirewallRule struct {
	Client string `json:"client,omitempty"` // client name; empty = all clients
	To     string `json:"to,omitempty"`     // network or address; empty = anywhere
	Proto  string `json:"proto,omitempty"`  // any (default), tcp, udp, tcp+udp, icmp
	Ports  string `json:"ports,omitempty"`  // "22", "80,443", "8000-8100" (tcp/udp only)
	Action string `json:"action"`           // allow or deny
	Note   string `json:"note,omitempty"`
}

// Firewall is the state of the access rules.
type Firewall struct {
	Policy   FirewallPolicy `json:"policy"`
	Mode     string         `json:"mode"`    // direct, file (applied by the OpenVPN container) or off
	Applied  bool           `json:"applied"` // the current rules are active
	Error    string         `json:"error,omitempty"`
	Warnings []string       `json:"warnings"`
	Missing  []string       `json:"missing"` // clients in rules without a fixed VPN address
	Subnet   string         `json:"subnet,omitempty"`
	Rules    string         `json:"rules"` // the generated iptables rules
	// ClientToClient: OpenVPN's server.conf has client-to-client, so traffic
	// between clients bypasses the rules (reported also while they are off).
	ClientToClient bool `json:"client_to_client,omitempty"`
}

// AssignAddresses asks for fixed VPN addresses for clients.
type AssignAddresses struct {
	Names []string `json:"names"`
}

// ToolRequest is the body of POST /v1/servers/{id}/tools/{tool}: ping,
// traceroute, route, dns, tcp or http, run in the server's network.
type ToolRequest struct {
	Target string `json:"target,omitempty"` // host name or IP (route: an IP, empty = the routing table)
	Count  int    `json:"count,omitempty"`  // ping: 1-10
	Ports  string `json:"ports,omitempty"`  // tcp: "22, 80,443" or "8000-8005"
	Kind   string `json:"kind,omitempty"`   // dns: A, AAAA, CNAME, MX, TXT, NS, PTR
	Server string `json:"server,omitempty"` // dns: a DNS server to ask (IP)
	URL    string `json:"url,omitempty"`    // http
}

// ToolResult is what a tool found.
type ToolResult struct {
	Tool      string `json:"tool"`
	OK        bool   `json:"ok"`
	Summary   string `json:"summary"`
	Output    string `json:"output"`
	Truncated bool   `json:"truncated,omitempty"`
	Millis    int64  `json:"duration_ms"`
}

// Speed is a speed limit in kbit/s (0 = unlimited), or blocked (the client
// stays connected but nothing gets through).
type Speed struct {
	Down  int64 `json:"down,omitempty"` // server → client
	Up    int64 `json:"up,omitempty"`   // client → server
	Block bool  `json:"block,omitempty"`
}

// Shaping is PUT /v1/servers/{id}/shaping: the speed each client may use
// now. The agent knows nothing of usage limits; the web part decides who is
// over and sends the result.
type Shaping struct {
	Default Speed            `json:"default"` // every client without its own
	Clients map[string]Speed `json:"clients"` // clients with their own speed
}

// ShapingState is GET /v1/servers/{id}/shaping and the answer to PUT.
type ShapingState struct {
	Shaping
	Mode    string `json:"mode"`    // direct, file (applied by the VPN container) or off
	Applied bool   `json:"applied"` // the current rules are active
	// Waiting: the server's interface is not up (stopped), the rules are
	// applied when it is.
	Waiting bool   `json:"waiting,omitempty"`
	Error   string `json:"error,omitempty"`
	// Addresses of the clients the rules match now (OpenVPN clients with
	// an automatic address only while connected).
	Addresses map[string]string `json:"addresses"`
	Script    string            `json:"script"` // the generated tc commands
}
