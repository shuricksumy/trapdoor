package agent

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

func TestNamedNetworks(t *testing.T) {
	dir := t.TempDir()
	p, err := pki.Init(filepath.Join(dir, "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	conf := "server 10.8.0.0 255.255.255.0\ntopology subnet\nclient-config-dir ccd\nroute 192.168.50.0 255.255.255.0\n"
	if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	ccdDir := filepath.Join(dir, "ccd")
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: ccdDir, MgmtAddr: "127.0.0.1:1"}
	srv := httptest.NewServer(serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), p)))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 10*time.Second).Server(DefaultServerID)
	ctx := context.Background()
	for _, n := range []string{"office-gw", "laptop", "phone"} {
		if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	ccd := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(ccdDir, name))
		return string(b)
	}
	// The laptop has a route of its own and 2FA in its file.
	if _, err := c.UpdateClientAuth(ctx, "laptop", api.ClientAuthUpdate{StaticPassword: "pin1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetClientNetwork(ctx, "laptop", api.ClientNetwork{Routes: []string{"10.20.0.0/16"}}); err != nil {
		t.Fatal(err)
	}

	// Office LAN behind office-gw, reachable for laptop and phone.
	res, err := c.SetNamedNetwork(ctx, "Office LAN", api.NamedNetwork{Description: "Main office",
		Networks: []string{"192.168.50.9/24"}, Via: "office-gw", Clients: []string{"phone", "laptop", "office-gw", "phone"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Networks) != 1 || res.Networks[0].Networks[0] != "192.168.50.0/24" || strings.Join(res.Networks[0].Clients, ",") != "laptop,phone" {
		t.Errorf("saved = %+v", res)
	}
	if !strings.Contains(ccd("office-gw"), "iroute 192.168.50.0 255.255.255.0") {
		t.Errorf("gateway:\n%s", ccd("office-gw"))
	}
	for _, n := range []string{"laptop", "phone"} {
		if !strings.Contains(ccd(n), `push "route 192.168.50.0 255.255.255.0"`) {
			t.Errorf("%s:\n%s", n, ccd(n))
		}
	}
	if !strings.Contains(ccd("laptop"), `push "route 10.20.0.0 255.255.0.0"`) || !strings.Contains(ccd("laptop"), "#STATIC_PASS_HASH:") {
		t.Errorf("laptop lost its own settings:\n%s", ccd("laptop"))
	}

	// The client's own settings leave the named ones out, and saving them keeps them.
	got, err := c.ClientNetwork(ctx, "laptop")
	if err != nil || strings.Join(got.Routes, ",") != "10.20.0.0/16" || len(got.Named) != 1 || got.Named[0] != (api.NamedUse{Name: "Office LAN", As: api.NamedRoute}) {
		t.Errorf("laptop network = %+v, %v", got, err)
	}
	if _, err := c.SetClientNetwork(ctx, "laptop", api.ClientNetwork{Routes: []string{"10.30.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if l := ccd("laptop"); !strings.Contains(l, "192.168.50.0") || !strings.Contains(l, "10.30.0.0") || strings.Contains(l, "10.20.0.0") {
		t.Errorf("laptop after its own change:\n%s", l)
	}
	if gw, _ := c.ClientNetwork(ctx, "office-gw"); len(gw.LAN) != 0 || len(gw.Named) != 1 || gw.Named[0].As != api.NamedLAN {
		t.Errorf("gateway network = %+v", gw)
	}
	// All clients at once (the Network page).
	all, err := c.ClientsNetwork(ctx)
	if err != nil || len(all) != 3 || all[0].Name != "laptop" || all[1].Name != "office-gw" || len(all[2].Named) != 1 {
		t.Errorf("clients network = %+v, %v", all, err)
	}
	if strings.Join(all[0].Routes, ",") != "10.30.0.0/16" || all[0].Named[0].Name != "Office LAN" {
		t.Errorf("laptop in the list = %+v", all[0])
	}

	// Rename and change: the phone leaves, a second network is added.
	if _, err := c.SetNamedNetwork(ctx, "Office LAN", api.NamedNetwork{Name: "Office", Networks: []string{"192.168.50.0/24", "192.168.51.0/24"},
		Via: "office-gw", Clients: []string{"laptop"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ccd("phone"), "192.168.50.0") {
		t.Errorf("phone kept the route:\n%s", ccd("phone"))
	}
	if !strings.Contains(ccd("laptop"), "192.168.51.0") || !strings.Contains(ccd("office-gw"), "iroute 192.168.51.0") {
		t.Errorf("new network missing:\nlaptop:\n%s\ngateway:\n%s", ccd("laptop"), ccd("office-gw"))
	}
	if res, _ := c.SetNamedNetwork(ctx, "Office", api.NamedNetwork{Networks: []string{"192.168.51.0/24"}, Via: "office-gw"}); len(res.Warnings) == 0 {
		t.Error("no warning for a network the server has no route for")
	}

	// Bad input changes nothing.
	for name, n := range map[string]api.NamedNetwork{
		"no networks":    {Name: "x"},
		"bad network":    {Name: "x", Networks: []string{"nope"}},
		"bad name":       {Name: "../x", Networks: []string{"10.1.0.0/16"}},
		"unknown client": {Name: "x", Networks: []string{"10.1.0.0/16"}, Clients: []string{"ghost"}},
		"vpn subnet lan": {Name: "x", Networks: []string{"10.8.0.0/16"}, Via: "phone"},
	} {
		if _, err := c.SetNamedNetwork(ctx, n.Name, n); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := c.SetNamedNetwork(ctx, "Home", api.NamedNetwork{Name: "Office", Networks: []string{"10.1.0.0/16"}}); !IsStatus(err, http.StatusConflict) {
		t.Errorf("duplicate name: %v", err)
	}
	if strings.Contains(ccd("phone"), "10.8.0.0") {
		t.Error("an invalid change was written")
	}

	// Remove: the routes go, the client's own stay.
	res, err = c.RemoveNamedNetwork(ctx, "Office")
	if err != nil || len(res.Networks) != 0 {
		t.Fatalf("remove = %+v, %v", res, err)
	}
	if l := ccd("laptop"); strings.Contains(l, "192.168.5") || !strings.Contains(l, "10.30.0.0") {
		t.Errorf("laptop after remove:\n%s", l)
	}
	if strings.Contains(ccd("office-gw"), "iroute") {
		t.Errorf("gateway after remove:\n%s", ccd("office-gw"))
	}
	if _, err := c.RemoveNamedNetwork(ctx, "Office"); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("remove twice: %v", err)
	}
}

// Naming routes a client already has, in another order and with comments
// (openvpn-ui's files), must not rewrite its file.
func TestNamedNetworkKeepsUnchangedFiles(t *testing.T) {
	dir := t.TempDir()
	p, err := pki.Init(filepath.Join(dir, "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	conf := "server 10.8.0.0 255.255.255.0\ntopology subnet\nclient-config-dir ccd\nclient-to-client\n"
	if err := os.WriteFile(filepath.Join(dir, "server.conf"), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	ccdDir := filepath.Join(dir, "ccd")
	cfg := config.Agent{Token: "secret", OpenVPNDir: dir, CCDDir: ccdDir, MgmtAddr: "127.0.0.1:1"}
	srv := httptest.NewServer(serve(New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), p)))
	t.Cleanup(srv.Close)
	c := NewClient(srv.URL, "secret", 10*time.Second).Server(DefaultServerID)
	ctx := context.Background()
	for _, n := range []string{"router", "laptop"} {
		if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"router": "### Automatic generated \"router\" settings file\nifconfig-push 10.8.0.20 255.255.255.0\niroute 192.168.50.0 255.255.255.0\n",
		"laptop": "### Automatic generated \"laptop\" settings file\n# Route to router\npush \"route 192.168.50.0 255.255.255.0\"\n# Route to docker\npush \"route 172.20.0.0 255.255.255.0\"\n#2FA_KEY:00\n",
	}
	if err := os.MkdirAll(ccdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, f := range files {
		if err := os.WriteFile(filepath.Join(ccdDir, n), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.SetNamedNetwork(ctx, "Router LAN", api.NamedNetwork{Networks: []string{"192.168.50.0/24"}, Via: "router", Clients: []string{"laptop"}}); err != nil {
		t.Fatal(err)
	}
	for n, f := range files {
		if b, _ := os.ReadFile(filepath.Join(ccdDir, n)); string(b) != f {
			t.Errorf("%s rewritten:\n%s\nwas:\n%s", n, b, f)
		}
	}
	// A real change still writes.
	if _, err := c.SetNamedNetwork(ctx, "Router LAN", api.NamedNetwork{Networks: []string{"192.168.50.0/24", "192.168.51.0/24"}, Via: "router", Clients: []string{"laptop"}}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(ccdDir, "laptop")); !strings.Contains(string(b), "192.168.51.0") || !strings.Contains(string(b), "#2FA_KEY:00") {
		t.Errorf("laptop after a change:\n%s", b)
	}
}
