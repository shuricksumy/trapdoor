package config

import (
	"errors"
	"testing"
	"time"
)

func TestLoadAgentDefaults(t *testing.T) {
	t.Setenv("TW_AGENT_TOKEN", "x")
	t.Setenv("TW_OPENVPN_DIR", "/srv/ovpn")
	c, err := LoadAgent()
	if err != nil {
		t.Fatal(err)
	}
	if c.PKIDir != "/srv/ovpn/easy-rsa/pki" {
		t.Errorf("PKIDir = %q, want derived from TW_OPENVPN_DIR", c.PKIDir)
	}
	if c.Listen != "127.0.0.1:9090" {
		t.Errorf("Listen = %q", c.Listen)
	}
}

func TestLoadAgentRequiresToken(t *testing.T) {
	t.Setenv("TW_AGENT_TOKEN", "")
	if _, err := LoadAgent(); !errors.Is(err, ErrNoToken) {
		t.Fatalf("err = %v, want ErrNoToken", err)
	}
}

func TestLoadWebTimeout(t *testing.T) {
	t.Setenv("TW_AGENT_TOKEN", "x")
	t.Setenv("TW_AGENT_TIMEOUT", "5s")
	c, err := LoadWeb()
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentTimeout != 5*time.Second {
		t.Errorf("AgentTimeout = %v", c.AgentTimeout)
	}

	t.Setenv("TW_AGENT_TIMEOUT", "soon")
	if _, err := LoadWeb(); err == nil {
		t.Error("expected error for invalid duration")
	}
}
