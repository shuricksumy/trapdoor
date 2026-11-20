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

	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestUptimeAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, 11, 20, 12, 20, 0, 0, time.UTC)
	srv := New(Deps{Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}), APIToken: "tok", Location: time.UTC,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }})
	h := srv.Handler()
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// anna 10:00-11:30 today, and online on wg0 (another server) now
	for i, row := range [][]any{{"openvpn", "anna", int64(1795168800), int64(1795174200)}, {"wg0", "carol", int64(1795170000), nil}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (agent, name, client_id, real_address, connected_at, last_seen, disconnected_at)
			VALUES (?, ?, ?, '', ?, ?, ?)`, row[0], row[1], i+1, row[2], row[2], row[3]); err != nil {
			t.Fatal(err)
		}
	}
	var rep history.UptimeReport
	rec := get("/api/v1/servers/openvpn/history/uptime?range=24h")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &rep) != nil {
		t.Fatalf("24h: %d %s", rec.Code, rec.Body)
	}
	// 24 hours ending with the current hour: 13:00 yesterday to 13:00 today
	if rep.Step != history.StepHour || len(rep.Starts) != 24 || !rep.To.Equal(time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("24h period: %+v", rep)
	}
	if len(rep.Clients) != 1 || rep.Clients[0].Name != "anna" || rep.Clients[0].OnlineSec != 90*60 {
		t.Fatalf("24h clients: %+v", rep.Clients)
	}
	if rec := get("/api/v1/servers/openvpn/history/uptime?range=7d"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"step":"hour"`) {
		t.Errorf("7d: %d", rec.Code)
	}
	rec = get("/api/v1/servers/openvpn/history/uptime?range=30d")
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Step != history.StepDay || len(rep.Starts) != 30 {
		t.Errorf("30d: %+v", rep.Starts)
	}
	rec = get("/api/v1/servers/openvpn/history/uptime?month=2026-11")
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	if rep.Step != history.StepDay || len(rep.Starts) != 30 || rep.Clients[0].Buckets[19] != 90*60 {
		t.Errorf("month: %+v", rep)
	}
	for _, bad := range []string{"range=1y", "month=11-2026"} {
		if rec := get("/api/v1/servers/openvpn/history/uptime?" + bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	// carol's open session belongs to wg0
	rec = get("/api/v1/servers/wg0/history/uptime?range=24h")
	_ = json.Unmarshal(rec.Body.Bytes(), &rep)
	if len(rep.Clients) != 1 || !rep.Clients[0].Online || rep.Clients[0].OnlineSec != 120*60 {
		t.Errorf("wg0: %+v", rep.Clients)
	}
}
