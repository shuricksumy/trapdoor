package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestServersOverviewAndControl(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(wgAgent(t, "local-secret"))
	t.Cleanup(local.Close)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	m, err := NewMultiAgent(ctx, agent.NewClient(local.URL, "local-secret", 5*time.Second), db, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.CreateServer(ctx, api.CreateServer{Type: api.TypeWireGuard, ID: "wg0", PublicHost: "vpn.example.com"}); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b"} {
		if _, err := m.Server("wg0").AddPeer(ctx, api.NewPeer{Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	disabled := true
	if _, err := m.Server("wg0").UpdatePeer(ctx, "b", api.PeerUpdate{Disabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)
	for _, hour := range []int64{now.Add(-2 * time.Hour).Unix(), now.Add(-20 * time.Hour).Unix()} { // today and yesterday
		if _, err := db.ExecContext(ctx, `INSERT INTO traffic_hourly (agent, name, hour, bytes_in, bytes_out) VALUES ('wg0', 'a', ?, 100, 200)`, hour-hour%3600); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(Deps{Agent: m, Hosts: m, Backup: m, Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}),
		APIToken: "tok", Location: time.UTC, Log: log, Now: func() time.Time { return now }}).Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	list := func() map[string]api.ServerSummary {
		rec := call(http.MethodGet, "/api/v1/servers", "")
		var out []api.ServerSummary
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("servers: %d %s", rec.Code, rec.Body)
		}
		byID := map[string]api.ServerSummary{}
		for _, s := range out {
			byID[s.ID] = s
		}
		return byID
	}
	wg0 := list()["wg0"]
	if wg0.Clients != 1 || wg0.TodayIn != 100 || wg0.TodayOut != 200 || wg0.Stopped || !wg0.Autostart {
		t.Errorf("wg0 = %+v", wg0)
	}
	if _, ok := list()["openvpn"]; !ok {
		t.Error("the OpenVPN server is missing")
	}

	if rec := call(http.MethodPost, "/api/v1/servers/wg0/stop", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stopped":true`) {
		t.Errorf("stop: %d %s", rec.Code, rec.Body)
	}
	if s := list()["wg0"]; !s.Stopped || s.Connected != 0 || s.SummaryError != "" {
		t.Errorf("stopped wg0 = %+v", s)
	}
	if rec := call(http.MethodPut, "/api/v1/servers/wg0/autostart", `{"autostart":false}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"autostart":false`) {
		t.Errorf("autostart: %d %s", rec.Code, rec.Body)
	}
	if rec := call(http.MethodPost, "/api/v1/servers/wg0/start", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"stopped":false`) {
		t.Errorf("start: %d %s", rec.Code, rec.Body)
	}
}
