// Package api contains the request/response types shared by the agent API
// and its clients (the web process). It must not import anything else from
// this module so both sides can depend on it freely.
package api

import "time"

// HeaderToken is the HTTP header carrying the shared agent token.
const HeaderToken = "X-Tunnelward-Token"

// Health is returned by GET /healthz on both processes.
type Health struct {
	Status string `json:"status"` // "ok"
}

// AgentInfo is returned by GET /v1/info on the agent.
type AgentInfo struct {
	Version    string `json:"version"`
	Hostname   string `json:"hostname"`
	OpenVPNDir string `json:"openvpn_dir"`
	PKIDir     string `json:"pki_dir"`
	PKIReady   bool   `json:"pki_ready"`
	// OpenVPNVersion is empty when the management interface is unreachable.
	OpenVPNVersion string `json:"openvpn_version,omitempty"`
	MgmtError      string `json:"mgmt_error,omitempty"`
}

// Status is returned by GET /api/v1/status on the web process.
type Status struct {
	WebVersion string     `json:"web_version"`
	AgentOK    bool       `json:"agent_ok"`
	Agent      *AgentInfo `json:"agent,omitempty"`
	AgentError string     `json:"agent_error,omitempty"`
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
