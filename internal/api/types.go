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
	Version  string       `json:"version"`
	Hostname string       `json:"hostname"`
	Servers  []ServerInfo `json:"servers"`
}

// ServerInfo describes one VPN server (GET /v1/servers/{id}/info).
type ServerInfo struct {
	// ID is the stable key used in URLs and in the history database.
	ID   string `json:"id"`
	Type string `json:"type"` // TypeOpenVPN
	Name string `json:"name"` // shown in the UI

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
)

// Obfuscation are AmneziaWG's [Interface] settings. Zero values are left
// out (AmneziaWG then behaves like plain WireGuard for that mechanism).
type Obfuscation struct {
	Level string `json:"level"` // AWGLevel15 or AWGLevel20
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
}

// CreateServer is the body of POST /v1/servers: a new WireGuard server.
type CreateServer struct {
	Type string `json:"type"` // TypeWireGuard or TypeAmneziaWG
	// ID is also the network interface name (e.g. wg0): lower-case letters,
	// digits and dashes, at most 15 characters.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Port is the UDP port the server listens on (default 51820).
	Port int `json:"port,omitempty"`
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
	// Live state from the interface.
	Connected     bool      `json:"connected"`
	Endpoint      string    `json:"endpoint,omitempty"`
	LastHandshake time.Time `json:"last_handshake,omitzero"`
	BytesReceived int64     `json:"bytes_received"` // from the client
	BytesSent     int64     `json:"bytes_sent"`     // to the client
}

// NewPeer is the body of POST /v1/servers/{id}/peers.
type NewPeer struct {
	Name string `json:"name"`
	// Address is optional; the next free address of the subnet by default.
	Address string   `json:"address,omitempty"`
	LAN     []string `json:"lan,omitempty"`
	// NoPSK skips the preshared key (it adds a layer against future
	// quantum attacks and is on by default).
	NoPSK bool `json:"no_psk,omitempty"`
}

// PeerUpdate is the body of PUT /v1/servers/{id}/peers/{name}.
type PeerUpdate struct {
	Disabled *bool    `json:"disabled,omitempty"`
	LAN      []string `json:"lan,omitempty"` // replaces the list when not nil
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
}

// IssueCertRequest is the body of POST /v1/certs.
type IssueCertRequest struct {
	Name         string `json:"name"`
	Kind         string `json:"kind,omitempty"`       // "client" (default) or "server"
	Passphrase   string `json:"passphrase,omitempty"` // encrypts the private key
	ValidityDays int    `json:"validity_days,omitempty"`
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
}

// AssignAddresses asks for fixed VPN addresses for clients.
type AssignAddresses struct {
	Names []string `json:"names"`
}
