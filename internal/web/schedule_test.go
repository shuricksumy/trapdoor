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
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestSlots(t *testing.T) {
	loc := time.FixedZone("X", 2*3600)
	at := func(s string) time.Time {
		tm, err := time.ParseInLocation("2006-01-02 15:04", s, loc)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	daily := BackupSchedule{Every: "daily", Hour: 3}
	weekly := BackupSchedule{Every: "weekly", Hour: 3, Weekday: 1} // Monday
	for _, c := range []struct {
		sc         BackupSchedule
		now        string
		last, next string
	}{
		{daily, "2026-11-19 02:59", "2026-11-18 03:00", "2026-11-19 03:00"},
		{daily, "2026-11-19 03:00", "2026-11-19 03:00", "2026-11-20 03:00"},
		{daily, "2026-11-19 23:00", "2026-11-19 03:00", "2026-11-20 03:00"},
		{weekly, "2026-11-19 12:00", "2026-11-16 03:00", "2026-11-23 03:00"}, // Thursday
		{weekly, "2026-11-16 02:00", "2026-11-09 03:00", "2026-11-16 03:00"}, // Monday, before
		{weekly, "2026-11-16 04:00", "2026-11-16 03:00", "2026-11-23 03:00"},
	} {
		now := at(c.now)
		if got := lastSlot(c.sc, now); !got.Equal(at(c.last)) {
			t.Errorf("%s %s: last = %s, want %s", c.sc.Every, c.now, got, c.last)
		}
		if got := nextSlot(c.sc, now); !got.Equal(at(c.next)) {
			t.Errorf("%s %s: next = %s, want %s", c.sc.Every, c.now, got, c.next)
		}
	}
}

func TestScheduledBackups(t *testing.T) {
	ctx := context.Background()
	local := httptest.NewServer(backupAgent(t, "local-secret", "local"))
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
	dir := filepath.Join(t.TempDir(), "backups")
	// A file of the user's own is never touched.
	_ = os.MkdirAll(dir, 0o700)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o600)

	var mu sync.Mutex
	now := time.Date(2026, 11, 19, 2, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	srv := New(Deps{Agent: m, Hosts: m, Backup: m, Stats: &history.Stats{DB: db}, Auth: auth.New(db, auth.Options{}),
		APIToken: "tok", Location: time.UTC, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: clock,
		BackupDir: dir, BackupPassphrase: "backup pass"})
	h := srv.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	info := func() BackupsInfo {
		rec := call(http.MethodGet, "/api/v1/backups", "")
		var bi BackupsInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &bi); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("backups: %d %s", rec.Code, rec.Body)
		}
		return bi
	}

	// Off by default: nothing runs.
	srv.backupIfDue(ctx)
	if bi := info(); bi.Schedule.Every != "off" || len(bi.Files) != 0 || bi.Next != nil || !bi.Encrypted {
		t.Fatalf("default: %+v", bi)
	}
	for _, bad := range []string{`{"every":"hourly","hour":3,"keep":2}`, `{"every":"daily","hour":24,"keep":2}`, `{"every":"daily","hour":3,"keep":0}`} {
		if rec := call(http.MethodPut, "/api/v1/backups/schedule", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", bad, rec.Code)
		}
	}
	if rec := call(http.MethodPut, "/api/v1/backups/schedule", `{"every":"daily","hour":3,"keep":2,"servers":true}`); rec.Code != http.StatusOK {
		t.Fatalf("schedule: %d %s", rec.Code, rec.Body)
	}
	if bi := info(); bi.Next == nil || !bi.Next.Equal(time.Date(2026, 11, 19, 3, 0, 0, 0, time.UTC)) {
		t.Fatalf("next: %+v", bi.Next)
	}
	// Saved at 02:00: yesterday's 03:00 does not run.
	srv.backupIfDue(ctx)
	if n := len(info().Files); n != 0 {
		t.Fatalf("ran before the first slot: %d files", n)
	}

	// Three days at 03:00: everything + each server every day, two of each kept.
	for day := 0; day < 3; day++ {
		advance(time.Hour)
		srv.backupIfDue(ctx)
		srv.backupIfDue(ctx) // the same slot runs once
		advance(23 * time.Hour)
	}
	bi := info()
	if bi.Last == nil || !bi.Last.OK || bi.Last.Manual || len(bi.Last.Files) != 3 {
		t.Fatalf("last: %+v", bi.Last)
	}
	kinds := map[string]int{}
	for _, f := range bi.Files {
		kinds[backupName.FindStringSubmatch(f.Name)[1]]++
		if !strings.HasSuffix(f.Name, ".tdbackup") {
			t.Errorf("not encrypted: %s", f.Name)
		}
	}
	want := map[string]int{"trapdoor-all": 2, "trapdoor-server-openvpn": 2, "trapdoor-server-wg0": 2}
	if len(kinds) != len(want) {
		t.Fatalf("kinds: %v", kinds)
	}
	for k, n := range want {
		if kinds[k] != n {
			t.Errorf("%s: %d files, want %d (%v)", k, kinds[k], n, kinds)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Error("own file removed")
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, ".partial-*")); len(ents) != 0 {
		t.Errorf("partial files left: %v", ents)
	}

	// The files are real backups: everything and one server.
	var all, wg string
	for _, f := range bi.Files {
		switch {
		case all == "" && strings.HasPrefix(f.Name, "trapdoor-all-"):
			all = f.Name
		case wg == "" && strings.HasPrefix(f.Name, "trapdoor-server-wg0-"):
			wg = f.Name
		}
	}
	rec := call(http.MethodGet, "/api/v1/backups/files/"+all, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("download: %d", rec.Code)
	}
	if files := readBackup(t, rec.Body.Bytes(), "backup pass"); files["agent/openvpn/easy-rsa/pki/private/ca.key"] != "CA KEY local" || files["web/trapdoor.db"] == "" {
		t.Errorf("everything backup: %v", keys(files))
	}
	rec = call(http.MethodGet, "/api/v1/backups/files/"+wg, "")
	if files := readBackup(t, rec.Body.Bytes(), "backup pass"); !strings.Contains(files["manifest.json"], `"kind": "server"`) {
		t.Errorf("server backup: %v", keys(files))
	}

	// Run now: a manual run, the scheduled slot still runs.
	// now: 02:00 on the fourth day
	if rec := call(http.MethodPost, "/api/v1/backups/run", ""); rec.Code != http.StatusOK {
		t.Fatalf("run now: %d %s", rec.Code, rec.Body)
	}
	if bi := info(); !bi.Last.Manual || bi.Next == nil || bi.Next.Hour() != 3 {
		t.Errorf("after run now: %+v next %v", bi.Last, bi.Next)
	}
	advance(time.Hour)
	srv.backupIfDue(ctx)
	if bi := info(); bi.Last.Manual {
		t.Error("scheduled run skipped after a manual one")
	}

	// Only backup file names: no other file, no path.
	for _, name := range []string{"notes.txt", "..%2Ftd.db", "trapdoor-all-1.tar.gz"} {
		if rec := call(http.MethodGet, "/api/v1/backups/files/"+name, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d", name, rec.Code)
		}
	}
	all = info().Files[0].Name // the older ones are pruned by now
	if rec := call(http.MethodDelete, "/api/v1/backups/files/"+all, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, all)); !os.IsNotExist(err) {
		t.Error("file not deleted")
	}

	// A failing backup is reported and not retried every minute.
	local.Close()
	advance(24 * time.Hour)
	srv.backupIfDue(ctx)
	if bi := info(); bi.Last.OK || bi.Last.Error == "" {
		t.Errorf("failure not reported: %+v", bi.Last)
	}
	if ents, _ := filepath.Glob(filepath.Join(dir, ".partial-*")); len(ents) != 0 {
		t.Errorf("partial files left after a failure: %v", ents)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
