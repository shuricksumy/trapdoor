package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/mgmt"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

type fakeMgmt struct {
	mu     sync.Mutex
	down   bool
	killed []string
	sigs   []string
}

func (f *fakeMgmt) err() error {
	if f.down {
		return fmt.Errorf("%w: dial refused", mgmt.ErrUnavailable)
	}
	return nil
}

func (f *fakeMgmt) Status(context.Context) (mgmt.Status, error) {
	return mgmt.Status{Sessions: []mgmt.Session{{ClientID: 3, Name: "alice", BytesSent: 10}}}, f.err()
}
func (f *fakeMgmt) LoadStats(context.Context) (mgmt.LoadStats, error) {
	return mgmt.LoadStats{Clients: 1, BytesIn: 5, BytesOut: 10}, f.err()
}
func (f *fakeMgmt) Version(context.Context) (string, error) { return "OpenVPN 2.6.19", f.err() }
func (f *fakeMgmt) KillClient(_ context.Context, id int64) error {
	if id != 3 {
		return fmt.Errorf("%w: client-kill %d: client-kill command failed", mgmt.ErrCommand, id)
	}
	return f.err()
}
func (f *fakeMgmt) Signal(_ context.Context, sig string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sigs = append(f.sigs, sig)
	return f.err()
}

func (f *fakeMgmt) KillName(_ context.Context, name string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, name)
	return 1, f.err()
}

func sessionClient(t *testing.T, fm *fakeMgmt) *Client {
	t.Helper()
	p, err := pki.Init(filepath.Join(t.TempDir(), "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(config.Agent{Token: "secret"}, slog.New(slog.NewTextHandler(io.Discard, nil)), p)
	s.mgmt = fm
	srv := httptest.NewServer(serve(s))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL, "secret", 5*time.Second).Server(DefaultServerID)
}

func TestSessions(t *testing.T) {
	fm := &fakeMgmt{}
	c := sessionClient(t, fm)
	ctx := context.Background()

	got, err := c.Sessions(ctx)
	if err != nil || got.Clients != 1 || len(got.Sessions) != 1 || got.Sessions[0].Name != "alice" {
		t.Fatalf("sessions = %+v, %v", got, err)
	}
	if err := c.KillSession(ctx, 3); err != nil {
		t.Errorf("kill 3: %v", err)
	}
	if err := c.KillSession(ctx, 99); !IsStatus(err, http.StatusNotFound) {
		t.Errorf("kill 99: err = %v, want 404", err)
	}
	if n, err := c.KillName(ctx, "alice"); err != nil || n != 1 {
		t.Errorf("kill alice = %d, %v", n, err)
	}
	if _, err := c.KillName(ctx, "a b"); !IsStatus(err, http.StatusBadRequest) {
		t.Errorf("bad name: err = %v", err)
	}
	info, err := c.ServerInfo(ctx)
	if err != nil || info.OpenVPNVersion != "OpenVPN 2.6.19" {
		t.Errorf("info = %+v, %v", info, err)
	}
}

func TestRevokeAndRemoveDisconnect(t *testing.T) {
	fm := &fakeMgmt{}
	c := sessionClient(t, fm)
	ctx := context.Background()
	for _, n := range []string{"alice", "bob"} {
		if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.RevokeCert(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := c.RemoveCert(ctx, "bob"); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(fm.killed) != "[alice bob]" {
		t.Errorf("killed = %v, want [alice bob]", fm.killed)
	}

	// Management down: revoke still succeeds, sessions endpoint reports 502.
	fm.down = true
	if _, err := c.IssueCert(ctx, api.IssueCertRequest{Name: "carol"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RevokeCert(ctx, "carol"); err != nil {
		t.Errorf("revoke with mgmt down: %v", err)
	}
	if _, err := c.Sessions(ctx); !IsStatus(err, http.StatusBadGateway) {
		t.Errorf("sessions with mgmt down: err = %v, want 502", err)
	}
	info, err := c.ServerInfo(ctx)
	if err != nil || info.MgmtError == "" || info.OpenVPNVersion != "" {
		t.Errorf("info with mgmt down = %+v, %v", info, err)
	}
}
