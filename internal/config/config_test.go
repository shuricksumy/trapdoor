package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAgentDefaults(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_OPENVPN_DIR", "/srv/ovpn")
	c, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if c.PKIDir != "/srv/ovpn/easy-rsa/pki" {
		t.Errorf("PKIDir = %q, want derived from TD_OPENVPN_DIR", c.PKIDir)
	}
	if c.Listen != "127.0.0.1:9090" {
		t.Errorf("Listen = %q", c.Listen)
	}
}

func TestLoadAgentRequiresToken(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "")
	if _, err := LoadAgent(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestLoadWebTimeout(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_AGENT_TIMEOUT", "5s")
	c, err := LoadWeb()
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentTimeout != 5*time.Second {
		t.Errorf("AgentTimeout = %v", c.AgentTimeout)
	}

	t.Setenv("TD_AGENT_TIMEOUT", "soon")
	if _, err := LoadWeb(); err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestLoadWebTZAndPoll(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_TZ", "Europe/Dublin")
	t.Setenv("TD_POLL_INTERVAL", "1m")
	c, err := LoadWeb()
	if err != nil || c.Location.String() != "Europe/Dublin" || c.PollInterval != time.Minute {
		t.Fatalf("c = %+v, %v", c, err)
	}
	t.Setenv("TD_TZ", "Mars/Olympus")
	if _, err := LoadWeb(); err == nil {
		t.Error("bad TZ accepted")
	}
	t.Setenv("TD_TZ", "")
	t.Setenv("TD_POLL_INTERVAL", "1s")
	if _, err := LoadWeb(); err == nil {
		t.Error("poll interval below 5s accepted")
	}
}

func TestLoadAgentOpenVPNOff(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	if c, err := LoadAgent(); err != nil || c.OpenVPN != "on" {
		t.Fatalf("default = %q, %v", c.OpenVPN, err)
	}
	t.Setenv("TD_OPENVPN", "off")
	if c, err := LoadAgent(); err != nil || c.OpenVPN != "off" {
		t.Fatalf("off = %q, %v", c.OpenVPN, err)
	}
	t.Setenv("TD_OPENVPN", "maybe")
	if _, err := LoadAgent(); err == nil {
		t.Error("TD_OPENVPN=maybe accepted")
	}
}

func TestLoadAgentJoin(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_JOIN_URL", "http://web:8080")
	if _, err := LoadAgent(); err == nil {
		t.Error("TD_JOIN_URL without the rest accepted")
	}
	t.Setenv("TD_JOIN_TOKEN", "tdj_x")
	t.Setenv("TD_NODE_NAME", "office")
	t.Setenv("TD_NODE_URL", "https://vpn-office:9443")
	t.Setenv("TD_MACHINE", "docker-host")
	if _, err := LoadAgent(); err == nil {
		t.Error("joining without TLS accepted")
	}
	t.Setenv("TD_AGENT_LISTEN", ":9443")
	t.Setenv("TD_AGENT_TLS", "auto")
	c, err := LoadAgent()
	if err != nil || c.JoinToken != "tdj_x" || c.NodeName != "office" || c.Machine != "docker-host" {
		t.Fatalf("join settings: %+v, %v", c, err)
	}
}

func TestLoadWebJoinSecret(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_JOIN_SECRET", "short")
	if _, err := LoadWeb(); err == nil {
		t.Error("a short TD_JOIN_SECRET accepted")
	}
	t.Setenv("TD_JOIN_SECRET", "a-secret-of-at-least-24-chars")
	if c, err := LoadWeb(); err != nil || c.JoinSecret == "" {
		t.Fatalf("secret: %v", err)
	}
}

func TestLoadWebFrameAncestors(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_FRAME_ANCESTORS", "https://admin.example.com, https://*.example.org:8443, 'self'")
	c, err := LoadWeb()
	if err != nil || len(c.FrameAncestors) != 3 {
		t.Fatalf("%v, %v", c.FrameAncestors, err)
	}
	for _, bad := range []string{"*", "https://a.example.com; script-src *", "javascript:alert(1)", "admin.example.com"} {
		t.Setenv("TD_FRAME_ANCESTORS", bad)
		if _, err := LoadWeb(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestLoadWebOIDC(t *testing.T) {
	t.Setenv("TD_AGENT_TOKEN", "x")
	t.Setenv("TD_OIDC_ISSUER", "https://auth.example.com/")
	if _, err := LoadWeb(); err == nil {
		t.Error("OIDC without TD_PUBLIC_URL accepted")
	}
	t.Setenv("TD_PUBLIC_URL", "https://vpn-admin.example.com/")
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("s3cret\n"), 0o600)
	t.Setenv("TD_OIDC_CLIENT_SECRET_FILE", secret)
	t.Setenv("TD_OIDC_GROUPS", "admin, vpn-admins")
	c, err := LoadWeb()
	if err != nil || c.OIDCIssuer != "https://auth.example.com" || c.OIDCClientSecret != "s3cret" || len(c.OIDCGroups) != 2 || c.OIDCName != "Authelia" {
		t.Fatalf("%+v %v", c, err)
	}
	t.Setenv("TD_OIDC_GROUPS", "")
	if c, _ := LoadWeb(); len(c.OIDCGroups) != 1 || c.OIDCGroups[0] != "admin" {
		t.Errorf("default group: %v", c.OIDCGroups)
	}
}
