package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestPeriodOf(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	now := time.Date(2026, 11, 19, 23, 30, 0, 0, time.UTC) // Fri Nov 20 01:30 in loc
	for _, c := range []struct{ period, start, end string }{
		{"day", "2026-11-20", "2026-11-21"},
		{"week", "2026-11-16", "2026-11-23"}, // Monday to Monday
		{"month", "2026-11-01", "2026-12-01"},
	} {
		s, e := periodOf(c.period, now, loc)
		if s.Format("2006-01-02") != c.start || e.Format("2006-01-02") != c.end || s.Location() != loc {
			t.Errorf("%s: %v – %v", c.period, s, e)
		}
	}
	// a Sunday belongs to the week that started on Monday
	if s, _ := periodOf("week", time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC), time.UTC); s.Day() != 16 {
		t.Errorf("sunday: %v", s)
	}
}

func TestSlower(t *testing.T) {
	for _, c := range [][3]int64{{1000, 0, 1000}, {1000, 500, 500}, {0, 500, 500}, {0, 0, 0}, {1000, 5000, 1000}} {
		if got := slower(c[0], c[1]); got != c[2] {
			t.Errorf("slower(%d, %d) = %d", c[0], c[1], got)
		}
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ovpnDir := t.TempDir()
	host := agent.NewHost("agent-secret", log, agent.New(config.Agent{Token: "agent-secret", OpenVPNDir: ovpnDir, CCDDir: t.TempDir(), MgmtAddr: "127.0.0.1:1"}, log, nil))
	ag := httptest.NewServer(host.Handler())
	t.Cleanup(ag.Close)
	client := agent.NewClient(ag.URL, "agent-secret", 5*time.Second)

	var mu sync.Mutex
	clock := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	tg := &tgbox{}
	srv := New(Deps{Agent: ClientAgent(client), Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}), APIToken: "tok",
		Location: time.UTC, Log: log, SendTelegram: tg.send,
		Now: func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }})
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	if err := db.SetSetting(ctx, settingTelegram, TelegramSettings{Enabled: true, Token: tgToken, Chats: []string{"4242"},
		Notify: Notify{UsageLimit: true}}); err != nil {
		t.Fatal(err)
	}
	use := func(name string, hour time.Time, bytes int64) {
		if _, err := db.Exec(`INSERT INTO traffic_hourly (agent, name, hour, bytes_in, bytes_out) VALUES ('openvpn', ?, ?, ?, 0)
			ON CONFLICT (agent, name, hour) DO UPDATE SET bytes_in = bytes_in + excluded.bytes_in`, name, hour.Unix(), bytes); err != nil {
			t.Fatal(err)
		}
	}
	agentGot := func() api.Shaping {
		var s api.Shaping
		b, err := os.ReadFile(filepath.Join(ovpnDir, "trapdoor-shaping.json"))
		if err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(b, &s)
		return s
	}
	row := func(l Limits, name string) LimitRow {
		for _, r := range l.Clients {
			if r.Name == name {
				return r
			}
		}
		t.Fatalf("no row for %s in %+v", name, l.Clients)
		return LimitRow{}
	}

	// Nothing set: built-in defaults, nobody limited, the agent untouched.
	rec := call(http.MethodGet, "/api/v1/servers/openvpn/limits", "")
	if l := decode[Limits](t, rec); rec.Code != 200 || l.Default != builtinLimit || len(l.Clients) != 0 || l.Shaping == nil {
		t.Fatalf("empty: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"down":-1}`, `{"period":"year"}`, `{"quota_mb":-5}`, `{"slow_up":99999999999}`} {
		if rec := call(http.MethodPut, "/api/v1/servers/openvpn/limits/default", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(http.MethodPut, "/api/v1/servers/openvpn/limits/clients/a%2Fb", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad name: %d", rec.Code)
	}

	// Defaults: 50 Mbit/s, 1 GB a month, then 1 Mbit/s. bob: 100 MB, then
	// blocked; carol: no speed limit of her own.
	nov := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	use("anna", nov, 900_000_000)
	use("bob", nov, 200_000_000)
	use("anna", time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC), 5_000_000_000) // last month: not counted
	if rec := call(http.MethodPut, "/api/v1/servers/openvpn/limits/default", `{"down":50000,"quota_mb":1000,"period":"month","slow_down":1000,"slow_up":1000}`); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	call(http.MethodPut, "/api/v1/servers/openvpn/limits/clients/bob", `{"quota_mb":100,"block":true}`)
	rec = call(http.MethodPut, "/api/v1/servers/openvpn/limits/clients/carol", `{"down":0}`)
	l := decode[Limits](t, rec)
	if a := row(l, "anna"); a.State != "capped" || a.Used != 900_000_000 || a.Speed != (api.Speed{Down: 50000}) || a.PeriodEnd.Format("2006-01-02") != "2026-12-01" {
		t.Errorf("anna: %+v", a)
	}
	if b := row(l, "bob"); b.State != "blocked" || !b.Speed.Block || b.Limit.QuotaMB != 100 {
		t.Errorf("bob: %+v", b)
	}
	if c := row(l, "carol"); c.State != "full" || c.Own.Down == nil || c.Limit.QuotaMB != 1000 {
		t.Errorf("carol: %+v", c)
	}
	got := agentGot()
	if got.Default != (api.Speed{Down: 50000}) || len(got.Clients) != 2 || !got.Clients["bob"].Block || got.Clients["carol"] != (api.Speed{}) {
		t.Errorf("agent got %+v", got)
	}

	// The minute check: anna at 90% and bob over get one message each, once.
	srv.checkLimits(ctx)
	srv.checkLimits(ctx)
	srv.mailWG.Wait()
	if len(tg.sent) != 2 || !strings.Contains(strings.Join(tg.sent, "\n"), "anna used 900.0 MB of its 1 GB per month on OpenVPN (90%)") ||
		!strings.Contains(strings.Join(tg.sent, "\n"), "⛔ bob used 200.0 MB of its 100 MB per month on OpenVPN and is blocked until Dec 1 00:00") {
		t.Errorf("messages: %q", tg.sent)
	}

	// anna goes over: slowed to 1 Mbit/s, one more message.
	use("anna", time.Date(2026, 11, 20, 11, 0, 0, 0, time.UTC), 200_000_000)
	srv.checkLimits(ctx)
	srv.mailWG.Wait()
	if got := agentGot(); got.Clients["anna"] != (api.Speed{Down: 1000, Up: 1000}) {
		t.Errorf("anna over: %+v", got)
	}
	if len(tg.sent) != 3 || !strings.Contains(tg.sent[2], "🐢 anna used 1.1 GB of its 1 GB per month on OpenVPN and is slowed down to 1 Mbit/s") {
		t.Errorf("messages: %q", tg.sent)
	}

	// Reset: anna counts from zero, back to her normal speed.
	rec = call(http.MethodPost, "/api/v1/servers/openvpn/limits/clients/anna/reset", "")
	if a := row(decode[Limits](t, rec), "anna"); a.State != "capped" || a.Used != 0 || a.ResetAt == nil {
		t.Errorf("after reset: %+v", a)
	}
	if got := agentGot(); got.Clients["anna"] != (api.Speed{}) && got.Clients["anna"].Down != 0 {
		t.Errorf("agent after reset: %+v", got)
	}
	if _, own := agentGot().Clients["anna"]; own {
		t.Error("anna still has her own speed at the agent")
	}
	use("anna", time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC), 50_000_000)
	mu.Lock()
	clock = time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)
	mu.Unlock()
	if a := row(decode[Limits](t, call(http.MethodGet, "/api/v1/servers/openvpn/limits", "")), "anna"); a.Used != 50_000_000 {
		t.Errorf("used after reset: %d", a.Used)
	}

	// A new month: bob's block ends.
	mu.Lock()
	clock = time.Date(2026, 12, 1, 0, 5, 0, 0, time.UTC)
	mu.Unlock()
	srv.checkLimits(ctx)
	if got := agentGot(); got.Clients["bob"].Block {
		t.Errorf("bob still blocked in December: %+v", got)
	}

	// carol back to the defaults: her entry goes.
	call(http.MethodPut, "/api/v1/servers/openvpn/limits/clients/carol", `{}`)
	all, _ := srv.loadLimits(ctx)
	if _, ok := all["openvpn"].Clients["carol"]; ok {
		t.Error("carol kept an empty entry")
	}
}
