// Package config loads trapdoor settings from environment variables.
//
// Every setting has a safe default so the binary starts with no configuration
// in development. All variables use the TD_ prefix.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Agent is the configuration of the privileged agent process.
type Agent struct {
	// Listen is the address the agent API binds to: "host:port" for TCP or
	// "unix:/path/to/socket" for a unix socket.            TD_AGENT_LISTEN
	Listen string
	// Token is the shared secret the web process must send. TD_AGENT_TOKEN
	Token string
	// SocketGID, when >= 0, becomes the group of the unix socket so a
	// non-root web container can connect (mode 0660).    TD_AGENT_SOCKET_GID
	SocketGID int
	// OpenVPNDir is the OpenVPN config directory.           TD_OPENVPN_DIR
	OpenVPNDir string
	// PKIDir is the easyrsa-compatible PKI directory.       TD_PKI_DIR
	PKIDir string
	// MgmtAddr is the OpenVPN management interface address. TD_OPENVPN_MGMT
	MgmtAddr string
	// MgmtPassword for "management ... pw-file" setups.  TD_OPENVPN_MGMT_PASSWORD
	MgmtPassword string
	// OpenVPNServersDir holds the OpenVPN servers added in trapdoor, one
	// directory each (<id>/server.conf, pki/, ccd/). TD_OPENVPN_SERVERS_DIR
	// (default <openvpn dir>/servers)
	OpenVPNServersDir string
	// Container names shown in the UI: where this agent's OpenVPN, WireGuard
	// and AmneziaWG servers run (e.g. "vpn", "vpn-wireguard"); empty = not
	// in a container.   TD_CONTAINER_OPENVPN, TD_CONTAINER_WIREGUARD, TD_CONTAINER_AMNEZIAWG
	ContainerOpenVPN, ContainerWireGuard, ContainerAmneziaWG string
	// Machine and NodeName are shown in the UI: the computer this agent runs
	// on and the node's name.              TD_MACHINE, TD_NODE_NAME
	Machine, NodeName string
	// JoinURL, JoinToken: the web UI this node adds itself to at start, with a
	// join token (from the UI, or the web UI's TD_JOIN_SECRET); NodeURL is how
	// the web UI reaches this agent (https://…:9443).
	// TD_JOIN_URL, TD_JOIN_TOKEN, TD_NODE_URL
	JoinURL, JoinToken, NodeURL string
	// OpenVPN: "off" runs no OpenVPN server on this agent (e.g. one that
	// only runs WireGuard); "on" (default).               TD_OPENVPN
	OpenVPN string
	// CCDDir holds per-client files (2FA lines).        TD_CCD_DIR (default <openvpn dir>/ccd)
	CCDDir string
	// TOTPIssuer is the name shown in authenticator apps. TD_TOTP_ISSUER
	TOTPIssuer string
	// OpenVPNBin is the openvpn binary (tls-crypt-v2 keys). TD_OPENVPN_BIN
	OpenVPNBin string
	// RestartSignal restarts OpenVPN via management: SIGHUP (re-read config)
	// or SIGTERM when a supervisor starts it again (Docker image). TD_OPENVPN_RESTART
	RestartSignal string
	// CAPassphrase unlocks an encrypted CA key.            TD_CA_PASSPHRASE
	CAPassphrase string
	// CRLCopies get a copy of crl.pem after every change
	// (comma-separated; "-" disables).                     TD_CRL_COPIES
	CRLCopies []string
	// Setup* describe a container that publishes a fixed port: the setup
	// wizard then keeps OpenVPN on SetupPort/SetupProto and suggests
	// SetupPublicPort to clients.  TD_SETUP_PORT, TD_SETUP_PROTO, TD_SETUP_PUBLIC_PORT
	SetupPort       int
	SetupProto      string
	SetupPublicPort int
	// Firewall: how access rules are applied: direct (iptables in this
	// process), file (the OpenVPN container applies them) or off. TD_FIREWALL
	Firewall string
	// Scripts lets the web UI edit each server's own start / stop scripts
	// (they run as root in the vpn container). Off: they are files on the
	// host, shown read-only.                                TD_SCRIPTS=on
	Scripts bool
	// IPTablesDir holds iptables/iptables-restore if not in PATH. TD_IPTABLES_DIR
	IPTablesDir string
	// WireGuard: how WireGuard servers are run: direct (wg / wg-quick in
	// this process), file (the VPN container applies the config and writes
	// `wg show` dumps) or off.                              TD_WIREGUARD
	WireGuard string
	// WireGuardDir holds <id>.json (trapdoor's state) and <id>.conf for
	// every WireGuard server.                               TD_WIREGUARD_DIR
	WireGuardDir string
	// AmneziaWGDir is the same for AmneziaWG servers (awg tools). TD_AMNEZIAWG_DIR
	AmneziaWGDir string
	// WGToolsDir holds wg / wg-quick / awg / awg-quick if not in PATH. TD_WG_TOOLS_DIR
	WGToolsDir string
	// TLS: "auto" serves the API with TLS on a TCP address, for a web
	// process on another host (self-signed certificate, pinned there by its
	// fingerprint); "off" (default) for a unix socket or localhost. TD_AGENT_TLS
	TLS string
	// TLSCert, TLSKey: the certificate files, created when missing.
	// TD_AGENT_TLS_CERT, TD_AGENT_TLS_KEY (default <openvpn dir>/trapdoor-agent.crt / .key)
	TLSCert, TLSKey string
}

// Web is the configuration of the web/API process.
type Web struct {
	// Listen is the HTTP address for browsers.              TD_WEB_LISTEN
	Listen string
	// AgentURL is where the agent API is reachable:
	// "http://host:port" or "unix:/path/to/socket".         TD_AGENT_URL
	AgentURL string
	// AgentToken must match the agent's TD_AGENT_TOKEN.     TD_AGENT_TOKEN
	AgentToken string
	// DBPath is the SQLite database file.                   TD_DB_PATH
	DBPath string
	// AgentTimeout bounds each call to the agent.           TD_AGENT_TIMEOUT
	AgentTimeout time.Duration
	// APIToken optionally lets scripts call /api/v1/* with
	// "Authorization: Bearer"; disabled if empty.         TD_WEB_API_TOKEN
	APIToken string
	// CookieSecure marks the login cookie HTTPS-only.     TD_COOKIE_SECURE (default true)
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (CIDRs).     TD_TRUSTED_PROXIES
	TrustedProxies []netip.Prefix
	// ExpiryWarnDays flags client certificates expiring
	// within this many days.                              TD_EXPIRY_WARN_DAYS (default 30)
	ExpiryWarnDays int
	// SessionIdle logs out inactive admins.               TD_SESSION_IDLE (default 12h)
	SessionIdle time.Duration
	// Location for day/month/year boundaries in reports.  TD_TZ (IANA, default local)
	Location *time.Location
	// PollInterval for recording session history.         TD_POLL_INTERVAL
	PollInterval time.Duration
	// JoinSecret lets nodes join with it instead of a one-time token, e.g.
	// the nodes of the same compose file (empty = tokens only). TD_JOIN_SECRET
	JoinSecret string
	// BackupDir: scheduled backups go here (TD_BACKUP_DIR, default "backups"
	// next to the database; "-" turns them off). BackupPassphrase encrypts
	// them (TD_BACKUP_PASSPHRASE).
	BackupDir, BackupPassphrase string
	// PublicURL is the web UI's address for links in emails (TD_PUBLIC_URL).
	PublicURL string
}

// ErrNoToken is returned when the shared agent token is not set.
var ErrNoToken = errors.New("TD_AGENT_TOKEN must be set")

// AgentDirs are the agent's directories from the environment, without the
// checks LoadAgent does (for commands like restore that need no token).
func AgentDirs() (openvpn, pki, ccd, servers, wireguard, amneziawg string) {
	openvpn = env("TD_OPENVPN_DIR", "/etc/openvpn")
	return openvpn, env("TD_PKI_DIR", openvpn+"/easy-rsa/pki"), env("TD_CCD_DIR", openvpn+"/ccd"),
		env("TD_OPENVPN_SERVERS_DIR", openvpn+"/servers"),
		env("TD_WIREGUARD_DIR", "/etc/wireguard"), env("TD_AMNEZIAWG_DIR", "/etc/amnezia/amneziawg")
}

// LoadAgent reads agent settings from the environment.
func LoadAgent() (Agent, error) {
	openvpnDir := env("TD_OPENVPN_DIR", "/etc/openvpn")
	gid := -1
	if v := os.Getenv("TD_AGENT_SOCKET_GID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Agent{}, fmt.Errorf("TD_AGENT_SOCKET_GID must be a group number")
		}
		gid = n
	}
	c := Agent{
		SocketGID:         gid,
		Listen:            env("TD_AGENT_LISTEN", "127.0.0.1:9090"),
		Token:             os.Getenv("TD_AGENT_TOKEN"),
		OpenVPNDir:        openvpnDir,
		PKIDir:            env("TD_PKI_DIR", openvpnDir+"/easy-rsa/pki"),
		MgmtAddr:          env("TD_OPENVPN_MGMT", "127.0.0.1:2080"),
		OpenVPNBin:        env("TD_OPENVPN_BIN", "openvpn"),
		CCDDir:            env("TD_CCD_DIR", openvpnDir+"/ccd"),
		OpenVPNServersDir: env("TD_OPENVPN_SERVERS_DIR", openvpnDir+"/servers"),
		RestartSignal:     env("TD_OPENVPN_RESTART", "SIGHUP"),
		TOTPIssuer:        env("TD_TOTP_ISSUER", "trapdoor"),
		MgmtPassword:      os.Getenv("TD_OPENVPN_MGMT_PASSWORD"),
		CAPassphrase:      os.Getenv("TD_CA_PASSPHRASE"),
		CRLCopies:         list(env("TD_CRL_COPIES", openvpnDir+"/crl.pem")),
	}
	for _, v := range []struct {
		name string
		dst  *int
	}{{"TD_SETUP_PORT", &c.SetupPort}, {"TD_SETUP_PUBLIC_PORT", &c.SetupPublicPort}} {
		if raw := os.Getenv(v.name); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 65535 {
				return c, fmt.Errorf("%s must be a port number", v.name)
			}
			*v.dst = n
		}
	}
	c.Scripts = env("TD_SCRIPTS", "off") == "on"
	c.Firewall = env("TD_FIREWALL", "direct")
	if c.Firewall != "direct" && c.Firewall != "file" && c.Firewall != "off" {
		return c, fmt.Errorf("TD_FIREWALL must be direct, file or off")
	}
	c.IPTablesDir = os.Getenv("TD_IPTABLES_DIR")
	c.Machine, c.NodeName = os.Getenv("TD_MACHINE"), os.Getenv("TD_NODE_NAME")
	c.JoinURL, c.JoinToken, c.NodeURL = os.Getenv("TD_JOIN_URL"), os.Getenv("TD_JOIN_TOKEN"), os.Getenv("TD_NODE_URL")
	c.ContainerOpenVPN = os.Getenv("TD_CONTAINER_OPENVPN")
	c.ContainerWireGuard = os.Getenv("TD_CONTAINER_WIREGUARD")
	c.ContainerAmneziaWG = os.Getenv("TD_CONTAINER_AMNEZIAWG")
	c.OpenVPN = env("TD_OPENVPN", "on")
	if c.OpenVPN != "on" && c.OpenVPN != "off" {
		return c, fmt.Errorf("TD_OPENVPN must be on or off")
	}
	c.WireGuard = env("TD_WIREGUARD", "direct")
	if c.WireGuard != "direct" && c.WireGuard != "file" && c.WireGuard != "off" {
		return c, fmt.Errorf("TD_WIREGUARD must be direct, file or off")
	}
	c.WireGuardDir = env("TD_WIREGUARD_DIR", "/etc/wireguard")
	c.AmneziaWGDir = env("TD_AMNEZIAWG_DIR", "/etc/amnezia/amneziawg")
	c.WGToolsDir = os.Getenv("TD_WG_TOOLS_DIR")
	c.TLS = env("TD_AGENT_TLS", "off")
	if c.TLS != "off" && c.TLS != "auto" {
		return c, fmt.Errorf("TD_AGENT_TLS must be off or auto")
	}
	if c.TLS == "auto" && strings.HasPrefix(c.Listen, "unix:") {
		return c, fmt.Errorf("TD_AGENT_TLS=auto needs a TCP TD_AGENT_LISTEN (e.g. :9443)")
	}
	c.TLSCert = env("TD_AGENT_TLS_CERT", openvpnDir+"/trapdoor-agent.crt")
	c.TLSKey = env("TD_AGENT_TLS_KEY", openvpnDir+"/trapdoor-agent.key")
	c.SetupProto = os.Getenv("TD_SETUP_PROTO")
	if c.SetupProto != "" && c.SetupProto != "udp" && c.SetupProto != "tcp" {
		return c, fmt.Errorf("TD_SETUP_PROTO must be udp or tcp")
	}
	if c.RestartSignal != "SIGHUP" && c.RestartSignal != "SIGTERM" {
		return c, fmt.Errorf("TD_OPENVPN_RESTART must be SIGHUP or SIGTERM")
	}
	if c.JoinURL != "" && (c.JoinToken == "" || c.NodeURL == "" || c.NodeName == "" || c.TLS != "auto") {
		return c, fmt.Errorf("TD_JOIN_URL needs TD_JOIN_TOKEN, TD_NODE_URL, TD_NODE_NAME and TD_AGENT_TLS=auto")
	}
	if c.Token == "" {
		return c, ErrNoToken
	}
	return c, nil
}

// LoadWeb reads web settings from the environment.
func LoadWeb() (Web, error) {
	timeout, err := time.ParseDuration(env("TD_AGENT_TIMEOUT", "30s"))
	if err != nil {
		return Web{}, fmt.Errorf("TD_AGENT_TIMEOUT: %w", err)
	}
	poll, err := time.ParseDuration(env("TD_POLL_INTERVAL", "30s"))
	if err != nil || poll < 5*time.Second {
		return Web{}, fmt.Errorf("TD_POLL_INTERVAL must be a duration of at least 5s")
	}
	loc := time.Local
	if tz := os.Getenv("TD_TZ"); tz != "" {
		if loc, err = time.LoadLocation(tz); err != nil {
			return Web{}, fmt.Errorf("TD_TZ: %w", err)
		}
	}
	idle, err := time.ParseDuration(env("TD_SESSION_IDLE", "12h"))
	if err != nil || idle < time.Minute {
		return Web{}, fmt.Errorf("TD_SESSION_IDLE must be a duration of at least 1m")
	}
	warnDays, err := strconv.Atoi(env("TD_EXPIRY_WARN_DAYS", "30"))
	if err != nil || warnDays < 1 || warnDays > 365 {
		return Web{}, fmt.Errorf("TD_EXPIRY_WARN_DAYS must be 1-365")
	}
	var proxies []netip.Prefix
	for _, p := range list(env("TD_TRUSTED_PROXIES", "-")) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return Web{}, fmt.Errorf("TD_TRUSTED_PROXIES: %q is not a CIDR or IP", p)
			}
			pfx = netip.PrefixFrom(addr, addr.BitLen())
		}
		proxies = append(proxies, pfx.Masked())
	}
	c := Web{
		Listen:         env("TD_WEB_LISTEN", ":8080"),
		AgentURL:       env("TD_AGENT_URL", "http://127.0.0.1:9090"),
		AgentToken:     os.Getenv("TD_AGENT_TOKEN"),
		DBPath:         env("TD_DB_PATH", "./data/trapdoor.db"),
		AgentTimeout:   timeout,
		APIToken:       os.Getenv("TD_WEB_API_TOKEN"),
		Location:       loc,
		PollInterval:   poll,
		CookieSecure:   env("TD_COOKIE_SECURE", "true") != "false",
		TrustedProxies: proxies,
		SessionIdle:    idle,
		JoinSecret:     os.Getenv("TD_JOIN_SECRET"),
	}
	c.BackupDir = env("TD_BACKUP_DIR", filepath.Join(filepath.Dir(c.DBPath), "backups"))
	if c.BackupDir == "-" {
		c.BackupDir = ""
	}
	c.BackupPassphrase = os.Getenv("TD_BACKUP_PASSPHRASE")
	c.PublicURL = strings.TrimRight(os.Getenv("TD_PUBLIC_URL"), "/")
	if c.JoinSecret != "" && len(c.JoinSecret) < 24 {
		return c, fmt.Errorf("TD_JOIN_SECRET must be at least 24 characters (openssl rand -hex 32)")
	}
	if c.AgentToken == "" {
		return c, ErrNoToken
	}
	return c, nil
}

// list splits a comma-separated value; "-" means an empty list.
func list(v string) []string {
	if v == "-" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
