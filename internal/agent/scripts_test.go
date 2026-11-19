package agent

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
)

func TestServerScripts(t *testing.T) {
	ctx := context.Background()
	for _, editable := range []bool{false, true} {
		c, h, cfg := ovpnHost(t)
		if editable {
			// TD_SCRIPTS=on on this agent.
			for _, b := range h.list() {
				if w, ok := b.(*WGServer); ok {
					w.scriptsEdit = true
				}
			}
		}
		if _, err := c.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com", Subnet: "10.9.0.0/24"}); err != nil {
			t.Fatal(err)
		}
		m, _ := h.get("wg0")
		m.b.(*WGServer).scriptsEdit = editable
		s := c.Server("wg0")

		got, err := s.Scripts(ctx)
		if err != nil || !got.Supported || got.Editable != editable || got.Iface != "wg0" || got.State != "" {
			t.Fatalf("empty: %+v, %v", got, err)
		}
		// Presets: rules in chain TDH-wg0 for the interface.
		got, err = s.SetScripts(ctx, api.Scripts{HookPresets: api.HookPresets{DNSRedirect: "172.30.0.53", BlockDoT: true, Isolate: true}})
		if err != nil {
			t.Fatal(err)
		}
		if got.DNSRedirect != "172.30.0.53" || !got.BlockDoT || !got.Isolate || got.State != "pending" {
			t.Errorf("presets: %+v", got)
		}
		up, _ := os.ReadFile(filepath.Join(cfg.WireGuardDir, "wg0.scripts-up.sh"))
		for _, want := range []string{
			`iptables -t nat -I PREROUTING 1 -i "$IFACE" -j TDH-wg0`,
			"--dport 53 ! -d 172.30.0.53 -j DNAT --to-destination 172.30.0.53:53",
			"-p tcp --dport 853 -j REJECT --reject-with tcp-reset",
			`iptables -A TDH-wg0 -o "$IFACE" -j REJECT`,
		} {
			if !strings.Contains(string(up), want) {
				t.Errorf("scripts-up.sh lacks %q:\n%s", want, up)
			}
		}
		if env, _ := os.ReadFile(filepath.Join(cfg.WireGuardDir, "wg0.scripts.env")); string(env) != "SERVER_ID=wg0\nTYPE=wireguard\nIFACE=wg0\n" {
			t.Errorf("env: %q", env)
		}
		if _, err := s.SetScripts(ctx, api.Scripts{HookPresets: api.HookPresets{DNSRedirect: "adguard"}}); !isStatus(err, http.StatusBadRequest) {
			t.Errorf("bad DNS address: %v", err)
		}

		// Own scripts: only with TD_SCRIPTS=on.
		own := api.Scripts{HookPresets: got.HookPresets, Up: "#!/bin/sh\necho up $IFACE\n", Down: "echo down\n"}
		got, err = s.SetScripts(ctx, own)
		if !editable {
			if !isStatus(err, http.StatusForbidden) || !strings.Contains(err.Error(), "wg0.up.sh and wg0.down.sh in this server's folder") {
				t.Fatalf("own script without TD_SCRIPTS: %v", err)
			}
			// Written on the host instead: shown, and kept by a presets change.
			_ = os.WriteFile(filepath.Join(cfg.WireGuardDir, "wg0.up.sh"), []byte(own.Up), 0o600)
			got, err = s.Scripts(ctx)
			if err != nil || got.Up != own.Up {
				t.Fatalf("host file: %+v, %v", got, err)
			}
			if _, err := s.SetScripts(ctx, api.Scripts{HookPresets: api.HookPresets{BlockDoT: true}, Up: own.Up}); err != nil {
				t.Errorf("presets with the host's script unchanged: %v", err)
			}
			continue
		}
		if err != nil || got.Up != own.Up || got.Down != own.Down {
			t.Fatalf("own scripts: %+v, %v", got, err)
		}

		// The vpn container's answer, for exactly these files.
		sc := m.b.(*WGServer).scripts()
		sum, _ := sc.sum()
		_ = os.WriteFile(sc.path("scripts.status"), []byte("error "+sum+" line 2: iptables: not found\n"), 0o600)
		if got, _ = s.Scripts(ctx); got.State != "error" || got.Message != "line 2: iptables: not found" {
			t.Errorf("error state: %+v", got)
		}
		_ = os.WriteFile(sc.path("scripts.status"), []byte("ok "+sum+"\n"), 0o600)
		if got, _ = s.Scripts(ctx); got.State != "ok" {
			t.Errorf("ok state: %+v", got)
		}
		_ = os.WriteFile(sc.path("scripts.status"), []byte("inactive\n"), 0o600)
		if got, _ = s.Scripts(ctx); got.State != "inactive" {
			t.Errorf("inactive state: %+v", got)
		}
		// Empty own scripts: the files go.
		if _, err := s.SetScripts(ctx, api.Scripts{}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sc.path("up.sh")); !os.IsNotExist(err) {
			t.Error("up.sh kept")
		}
		// Removing the server removes them.
		if err := s.RemoveServer(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(sc.path("scripts-up.sh")); !os.IsNotExist(err) {
			t.Error("scripts kept after remove")
		}
	}
}

func isStatus(err error, status int) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == status
}
