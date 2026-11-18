// Package config loads tunnelward settings from environment variables.
//
// Every setting has a safe default so the binary starts with no configuration
// in development. All variables use the TW_ prefix.
package config

import (
	"errors"
	"fmt"
	"os"
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
	// APIToken protects /api/v1/* (Authorization: Bearer)
	// until admin login exists; API disabled if empty.    TW_WEB_API_TOKEN
	APIToken string
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
	c := Agent{
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
	c := Web{
		Listen:       env("TW_WEB_LISTEN", ":8080"),
		AgentURL:     env("TW_AGENT_URL", "http://127.0.0.1:9090"),
		AgentToken:   os.Getenv("TW_AGENT_TOKEN"),
		DBPath:       env("TW_DB_PATH", "./data/tunnelward.db"),
		AgentTimeout: timeout,
		APIToken:     os.Getenv("TW_WEB_API_TOKEN"),
		Location:     loc,
		PollInterval: poll,
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
