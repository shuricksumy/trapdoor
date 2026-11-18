// Package config loads tunnelward settings from environment variables.
//
// Every setting has a safe default so the binary starts with no configuration
// in development. All variables use the TW_ prefix.
package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Agent is the configuration of the privileged agent process.
type Agent struct {
	// Listen is the address the agent API binds to: "host:port" for TCP or
	// "unix:/path/to/socket" for a unix socket.            TW_AGENT_LISTEN
	Listen string
	// Token is the shared secret the web process must send. TW_AGENT_TOKEN
	Token string
	// SocketGID, when >= 0, becomes the group of the unix socket so a
	// non-root web container can connect (mode 0660).    TW_AGENT_SOCKET_GID
	SocketGID int
	// OpenVPNDir is the OpenVPN config directory.           TW_OPENVPN_DIR
	OpenVPNDir string
	// PKIDir is the easyrsa-compatible PKI directory.       TW_PKI_DIR
	PKIDir string
	// MgmtAddr is the OpenVPN management interface address. TW_OPENVPN_MGMT
	MgmtAddr string
	// MgmtPassword for "management ... pw-file" setups.  TW_OPENVPN_MGMT_PASSWORD
	MgmtPassword string
	// OpenVPNBin is the openvpn binary (tls-crypt-v2 keys). TW_OPENVPN_BIN
	OpenVPNBin string
	// CAPassphrase unlocks an encrypted CA key.            TW_CA_PASSPHRASE
	CAPassphrase string
	// CRLCopies get a copy of crl.pem after every change
	// (comma-separated; "-" disables).                     TW_CRL_COPIES
	CRLCopies []string
}

// Web is the configuration of the web/API process.
type Web struct {
	// Listen is the HTTP address for browsers.              TW_WEB_LISTEN
	Listen string
	// AgentURL is where the agent API is reachable:
	// "http://host:port" or "unix:/path/to/socket".         TW_AGENT_URL
	AgentURL string
	// AgentToken must match the agent's TW_AGENT_TOKEN.     TW_AGENT_TOKEN
	AgentToken string
	// DBPath is the SQLite database file.                   TW_DB_PATH
	DBPath string
	// AgentTimeout bounds each call to the agent.           TW_AGENT_TIMEOUT
	AgentTimeout time.Duration
	// APIToken optionally lets scripts call /api/v1/* with
	// "Authorization: Bearer"; disabled if empty.         TW_WEB_API_TOKEN
	APIToken string
	// CookieSecure marks the login cookie HTTPS-only.     TW_COOKIE_SECURE (default true)
	CookieSecure bool
	// TrustedProxies may set X-Forwarded-For (CIDRs).     TW_TRUSTED_PROXIES
	TrustedProxies []netip.Prefix
	// SessionIdle logs out inactive admins.               TW_SESSION_IDLE (default 12h)
	SessionIdle time.Duration
	// Location for day/month/year boundaries in reports.  TW_TZ (IANA, default local)
	Location *time.Location
	// PollInterval for recording session history.         TW_POLL_INTERVAL
	PollInterval time.Duration
}

// ErrNoToken is returned when the shared agent token is not set.
var ErrNoToken = errors.New("TW_AGENT_TOKEN must be set")

// LoadAgent reads agent settings from the environment.
func LoadAgent() (Agent, error) {
	openvpnDir := env("TW_OPENVPN_DIR", "/etc/openvpn")
	gid := -1
	if v := os.Getenv("TW_AGENT_SOCKET_GID"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return Agent{}, fmt.Errorf("TW_AGENT_SOCKET_GID must be a group number")
		}
		gid = n
	}
	c := Agent{
		SocketGID:    gid,
		Listen:       env("TW_AGENT_LISTEN", "127.0.0.1:9090"),
		Token:        os.Getenv("TW_AGENT_TOKEN"),
		OpenVPNDir:   openvpnDir,
		PKIDir:       env("TW_PKI_DIR", openvpnDir+"/easy-rsa/pki"),
		MgmtAddr:     env("TW_OPENVPN_MGMT", "127.0.0.1:2080"),
		OpenVPNBin:   env("TW_OPENVPN_BIN", "openvpn"),
		MgmtPassword: os.Getenv("TW_OPENVPN_MGMT_PASSWORD"),
		CAPassphrase: os.Getenv("TW_CA_PASSPHRASE"),
		CRLCopies:    list(env("TW_CRL_COPIES", openvpnDir+"/crl.pem")),
	}
	if c.Token == "" {
		return c, ErrNoToken
	}
	return c, nil
}

// LoadWeb reads web settings from the environment.
func LoadWeb() (Web, error) {
	timeout, err := time.ParseDuration(env("TW_AGENT_TIMEOUT", "30s"))
	if err != nil {
		return Web{}, fmt.Errorf("TW_AGENT_TIMEOUT: %w", err)
	}
	poll, err := time.ParseDuration(env("TW_POLL_INTERVAL", "30s"))
	if err != nil || poll < 5*time.Second {
		return Web{}, fmt.Errorf("TW_POLL_INTERVAL must be a duration of at least 5s")
	}
	loc := time.Local
	if tz := os.Getenv("TW_TZ"); tz != "" {
		if loc, err = time.LoadLocation(tz); err != nil {
			return Web{}, fmt.Errorf("TW_TZ: %w", err)
		}
	}
	idle, err := time.ParseDuration(env("TW_SESSION_IDLE", "12h"))
	if err != nil || idle < time.Minute {
		return Web{}, fmt.Errorf("TW_SESSION_IDLE must be a duration of at least 1m")
	}
	var proxies []netip.Prefix
	for _, p := range list(env("TW_TRUSTED_PROXIES", "-")) {
		pfx, err := netip.ParsePrefix(p)
		if err != nil {
			addr, err2 := netip.ParseAddr(p)
			if err2 != nil {
				return Web{}, fmt.Errorf("TW_TRUSTED_PROXIES: %q is not a CIDR or IP", p)
			}
			pfx = netip.PrefixFrom(addr, addr.BitLen())
		}
		proxies = append(proxies, pfx.Masked())
	}
	c := Web{
		Listen:         env("TW_WEB_LISTEN", ":8080"),
		AgentURL:       env("TW_AGENT_URL", "http://127.0.0.1:9090"),
		AgentToken:     os.Getenv("TW_AGENT_TOKEN"),
		DBPath:         env("TW_DB_PATH", "./data/tunnelward.db"),
		AgentTimeout:   timeout,
		APIToken:       os.Getenv("TW_WEB_API_TOKEN"),
		Location:       loc,
		PollInterval:   poll,
		CookieSecure:   env("TW_COOKIE_SECURE", "true") != "false",
		TrustedProxies: proxies,
		SessionIdle:    idle,
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
