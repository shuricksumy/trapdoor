// Package config loads tunnelward settings from environment variables.
//
// Every setting has a safe default so the binary starts with no configuration
// in development. All variables use the TW_ prefix.
package config

import (
	"errors"
	"fmt"
	"os"
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
}

// ErrNoToken is returned when the shared agent token is not set.
var ErrNoToken = errors.New("TW_AGENT_TOKEN must be set")

// LoadAgent reads agent settings from the environment.
func LoadAgent() (Agent, error) {
	openvpnDir := env("TW_OPENVPN_DIR", "/etc/openvpn")
	c := Agent{
		Listen:     env("TW_AGENT_LISTEN", "127.0.0.1:9090"),
		Token:      os.Getenv("TW_AGENT_TOKEN"),
		OpenVPNDir: openvpnDir,
		PKIDir:     env("TW_PKI_DIR", openvpnDir+"/easy-rsa/pki"),
		MgmtAddr:   env("TW_OPENVPN_MGMT", "127.0.0.1:2080"),
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
	c := Web{
		Listen:       env("TW_WEB_LISTEN", ":8080"),
		AgentURL:     env("TW_AGENT_URL", "http://127.0.0.1:9090"),
		AgentToken:   os.Getenv("TW_AGENT_TOKEN"),
		DBPath:       env("TW_DB_PATH", "./data/tunnelward.db"),
		AgentTimeout: timeout,
	}
	if c.AgentToken == "" {
		return c, ErrNoToken
	}
	return c, nil
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
