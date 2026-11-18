// Package api contains the request/response types shared by the agent API
// and its clients (the web process). It must not import anything else from
// this module so both sides can depend on it freely.
package api

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
