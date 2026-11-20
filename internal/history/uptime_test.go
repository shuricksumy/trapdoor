package history

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/store"
)

func TestUptime(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) int64 {
		return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Unix()
	}
	id := 0
	add := func(agent, name string, from, to int64, open bool) {
		id++
		var disc any = to
		if open {
			disc = nil
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (agent, name, client_id, real_address, connected_at, last_seen, disconnected_at)
			VALUES (?, ?, ?, '203.0.113.7:1', ?, ?, ?)`, agent, name, id, from, to, disc); err != nil {
			t.Fatal(err)
		}
	}
	add("openvpn", "anna", at(1, 30), at(3, 15), false)   // 1:30-3:15
	add("openvpn", "anna", at(5, 0), at(5, 10), false)    // 5:00-5:10
	add("openvpn", "bob", at(-2, 0), at(0, 30), false)    // from yesterday, 0:00-0:30 inside
	add("openvpn", "home", at(-48, 0), at(-47, 0), false) // two days ago: outside, but last seen
	add("openvpn", "router", at(10, 0), at(10, 0), true)  // still open at "now" 12:00
	add("wg0", "anna", at(1, 0), at(9, 0), false)         // another server: not counted

	now := base.Add(12 * time.Hour)
	st := &Stats{DB: db, Agent: "openvpn"}
	r, err := st.Uptime(ctx, base, base.Add(24*time.Hour), StepHour, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Starts) != 24 || !r.Starts[1].Equal(base.Add(time.Hour)) {
		t.Fatalf("starts: %v", r.Starts)
	}
	got := map[string]ClientUptime{}
	for _, c := range r.Clients {
		got[c.Name] = c
	}
	a := got["anna"]
	if a.Sessions != 2 || a.OnlineSec != (105+10)*60 || a.Buckets[1] != 30*60 || a.Buckets[2] != 3600 || a.Buckets[3] != 15*60 || a.Buckets[5] != 10*60 || a.Online {
		t.Errorf("anna: %+v", a)
	}
	if b := got["bob"]; b.OnlineSec != 30*60 || b.Buckets[0] != 30*60 || b.Sessions != 1 {
		t.Errorf("bob (clipped at the start): %+v", b)
	}
	// still connected: counts until now, not past it
	if rt := got["router"]; !rt.Online || rt.OnlineSec != 2*3600 || rt.Buckets[10] != 3600 || rt.Buckets[11] != 3600 || rt.Buckets[12] != 0 || !rt.LastSeen.Equal(now) {
		t.Errorf("router (open): %+v", rt)
	}
	// seen before the period only: listed with zero time and its last seen
	if h := got["home"]; h.OnlineSec != 0 || h.Sessions != 0 || h.LastSeen == nil || !h.LastSeen.Equal(base.Add(-47*time.Hour)) {
		t.Errorf("home: %+v", h)
	}
	if a.LastSeen == nil || !a.LastSeen.Equal(base.Add(5*time.Hour+10*time.Minute)) {
		t.Errorf("anna last seen: %v", a.LastSeen)
	}

	// Days in a time zone: the day boundary is local midnight.
	loc := time.FixedZone("UTC+2", 2*3600)
	from := time.Date(2026, 11, 19, 0, 0, 0, 0, loc)
	r, err = st.Uptime(ctx, from, from.AddDate(0, 0, 2), StepDay, loc, now)
	if err != nil {
		t.Fatal(err)
	}
	got = map[string]ClientUptime{}
	for _, c := range r.Clients {
		got[c.Name] = c
	}
	// bob 22:00-00:30 UTC = 00:00-02:30 local on the 20th; all of anna's on the 20th too
	if b := got["bob"]; len(b.Buckets) != 2 || b.Buckets[0] != 0 || b.Buckets[1] != 150*60 {
		t.Errorf("bob by local day: %+v", b)
	}

	if _, err := st.Uptime(ctx, base, base, StepHour, time.UTC, now); !errors.Is(err, ErrBadQuery) {
		t.Errorf("empty period: %v", err)
	}
	if _, err := st.Uptime(ctx, base, base.Add(time.Hour), Step("week"), time.UTC, now); !errors.Is(err, ErrBadQuery) {
		t.Errorf("bad step: %v", err)
	}
}

func TestEvents(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	base := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	at := func(h, m int) int64 {
		return base.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute).Unix()
	}
	id := 0
	add := func(name string, from, to int64) {
		id++
		var disc any = to
		if to == 0 {
			disc = nil
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (agent, name, client_id, real_address, connected_at, last_seen, disconnected_at)
			VALUES ('openvpn', ?, ?, '203.0.113.7:1', ?, ?, ?)`, name, id, from, max(from, to), disc); err != nil {
			t.Fatal(err)
		}
	}
	add("anna", at(8, 0), at(9, 0))    // up 8:00, down 9:00
	add("anna", at(9, 1), at(12, 0))   // a quick reconnect (1 min), then down 12:00
	add("bob", at(-1, 0), at(1, 0))    // up yesterday: only its down is in the day
	add("bob", at(20, 0), 0)           // still up
	add("carol", at(-3, 0), at(-2, 0)) // before the day: nothing
	st := &Stats{DB: db, Agent: "openvpn"}
	day := func(quiet time.Duration, name string) []Event {
		ev, total, err := st.Events(ctx, base, base.Add(24*time.Hour), name, quiet, 0, 100)
		if err != nil || total != len(ev) {
			t.Fatalf("events: %v %d", err, total)
		}
		return ev
	}
	short := func(ev []Event) []string {
		out := []string{}
		for _, e := range ev {
			k := "down"
			if e.Up {
				k = "up"
			}
			out = append(out, e.Time.Format("15:04")+" "+e.Name+" "+k)
		}
		return out
	}
	got := short(day(0, ""))
	want := []string{"20:00 bob up", "12:00 anna down", "09:01 anna up", "09:00 anna down", "08:00 anna up", "01:00 bob down"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("all:\n%v\nwant\n%v", got, want)
	}
	// quiet 2 minutes: anna's reconnect folds into one session 8:00-12:00
	ev := day(2*time.Minute, "anna")
	if strings.Join(short(ev), ",") != "12:00 anna down,08:00 anna up" || ev[0].Lasted != 4*3600 || ev[1].Merged != 1 || ev[1].From != "203.0.113.7:1" {
		t.Errorf("quiet: %+v", ev)
	}
	// paging, newest first
	page, total, _ := st.Events(ctx, base, base.Add(24*time.Hour), "", 0, 2, 2)
	if total != 6 || strings.Join(short(page), ",") != "09:01 anna up,09:00 anna down" {
		t.Errorf("page: %v of %d", short(page), total)
	}
	if page, _, _ := st.Events(ctx, base, base.Add(24*time.Hour), "", 0, 50, 2); len(page) != 0 {
		t.Errorf("past the end: %v", page)
	}
	if _, _, err := st.Events(ctx, base, base, "", 0, 0, 10); !errors.Is(err, ErrBadQuery) {
		t.Errorf("empty period: %v", err)
	}
}
