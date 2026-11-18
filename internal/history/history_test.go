package history

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/store"
)

type fakeSource struct {
	sessions []api.Session
	err      error
}

func (f *fakeSource) Sessions(context.Context) (api.Sessions, error) {
	return api.Sessions{Sessions: f.sessions}, f.err
}

type env struct {
	t    *testing.T
	db   *store.DB
	src  *fakeSource
	col  *Collector
	now  time.Time
	stat *Stats
}

func newEnv(t *testing.T, start time.Time) *env {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	e := &env{t: t, db: db, src: &fakeSource{}, now: start}
	e.col = &Collector{DB: db, Source: e.src, Agent: "default", Interval: 30 * time.Second,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return e.now }}
	e.stat = &Stats{DB: db, Agent: "default"}
	return e
}

func (e *env) poll(at time.Time) {
	e.t.Helper()
	e.now = at
	if err := e.col.Poll(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) hourly() map[int64][3]int64 { // hour → in, out, estimated
	e.t.Helper()
	rows, err := e.db.Query(`SELECT hour, SUM(bytes_in), SUM(bytes_out), MAX(estimated) FROM traffic_hourly GROUP BY hour`)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[int64][3]int64{}
	for rows.Next() {
		var h, in, o, est int64
		if err := rows.Scan(&h, &in, &o, &est); err != nil {
			e.t.Fatal(err)
		}
		out[h] = [3]int64{in, o, est}
	}
	return out
}

func sess(id int64, name string, since time.Time, in, out int64) api.Session {
	return api.Session{ClientID: id, Name: name, RealAddress: "1.2.3.4:5", VirtualAddress: "10.8.0.2",
		ConnectedSince: since, BytesReceived: in, BytesSent: out}
}

var t0 = time.Date(2026, 11, 18, 10, 0, 5, 0, time.UTC)

func TestCollectorNormalPolling(t *testing.T) {
	e := newEnv(t, t0)
	conn := t0.Add(-10 * time.Second)
	e.src.sessions = []api.Session{sess(1, "alice", conn, 100, 200)}
	e.poll(t0)
	e.src.sessions = []api.Session{sess(1, "alice", conn, 400, 600)}
	e.poll(t0.Add(30 * time.Second))
	e.src.sessions = nil
	e.poll(t0.Add(60 * time.Second))

	h := e.hourly()
	if got := h[hourOf(t0.Unix())]; got != [3]int64{400, 600, 0} {
		t.Errorf("hour bucket = %v, want 400/600 not estimated", got)
	}
	var disc, seen int64
	if err := e.db.QueryRow(`SELECT disconnected_at, last_seen FROM sessions`).Scan(&disc, &seen); err != nil {
		t.Fatal(err)
	}
	if disc != t0.Add(30*time.Second).Unix() || seen != disc {
		t.Errorf("disconnected_at = %d, want last seen %d", disc, t0.Add(30*time.Second).Unix())
	}
}

func TestCollectorGapIsSpreadAndEstimated(t *testing.T) {
	e := newEnv(t, t0)
	conn := t0
	e.src.sessions = []api.Session{sess(1, "alice", conn, 0, 0)}
	e.poll(t0)
	// Web down for 3 hours; 10800 bytes in total meanwhile.
	e.src.sessions = []api.Session{sess(1, "alice", conn, 10800, 21600)}
	e.poll(t0.Add(3 * time.Hour))

	h := e.hourly()
	var in, out int64
	for hour, v := range h {
		in += v[0]
		out += v[1]
		if v[2] != 1 {
			t.Errorf("hour %d not marked estimated", hour)
		}
	}
	if in != 10800 || out != 21600 {
		t.Errorf("total = %d/%d, want 10800/21600 (nothing lost to rounding)", in, out)
	}
	if len(h) != 4 { // 10:00 (partial), 11, 12, 13:00 (partial)
		t.Errorf("buckets = %d, want 4: %v", len(h), h)
	}
	if v := h[hourOf(t0.Unix())+3600]; v[0] != 3600 {
		t.Errorf("full hour got %d, want 3600", v[0])
	}
}

func TestCollectorAgentDownChangesNothing(t *testing.T) {
	e := newEnv(t, t0)
	e.src.sessions = []api.Session{sess(1, "alice", t0, 10, 10)}
	e.poll(t0)
	e.src.err = errors.New("agent unreachable")
	e.now = t0.Add(time.Minute)
	if err := e.col.Poll(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	var open int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE disconnected_at IS NULL`).Scan(&open); err != nil || open != 1 {
		t.Errorf("open sessions = %d, %v; must stay open while agent is down", open, err)
	}
}

func TestCollectorReconnectIsNewSession(t *testing.T) {
	e := newEnv(t, t0)
	e.src.sessions = []api.Session{sess(1, "alice", t0, 100, 100)}
	e.poll(t0)
	// Reconnected: new client id, counters start again.
	e.src.sessions = []api.Session{sess(2, "alice", t0.Add(20*time.Second), 50, 50)}
	e.poll(t0.Add(30 * time.Second))

	var n, closed int
	_ = e.db.QueryRow(`SELECT COUNT(*), COUNT(disconnected_at) FROM sessions`).Scan(&n, &closed)
	if n != 2 || closed != 1 {
		t.Errorf("sessions = %d (closed %d), want 2 (1 closed)", n, closed)
	}
	if got := e.hourly()[hourOf(t0.Unix())]; got[0] != 150 {
		t.Errorf("traffic = %v, want 150 in", got)
	}
}

func TestReport(t *testing.T) {
	dublin, err := time.LoadLocation("Europe/Dublin")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, t0)
	day1 := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

	e.src.sessions = []api.Session{sess(1, "alice", day1, 0, 0), sess(2, "bob", day1, 0, 0)}
	e.poll(day1)
	e.src.sessions = []api.Session{sess(1, "alice", day1, 1000, 3000), sess(2, "bob", day1, 500, 500)}
	e.poll(day1.Add(30 * time.Second))
	e.src.sessions = nil
	e.poll(day1.Add(time.Minute))

	e.src.sessions = []api.Session{sess(3, "alice", day2, 0, 0)}
	e.poll(day2)
	e.src.sessions = []api.Session{sess(3, "alice", day2, 10, 20)}
	e.poll(day2.Add(30 * time.Second))

	ctx := context.Background()
	r, err := e.stat.Report(ctx, Query{
		From: time.Date(2026, 11, 1, 0, 0, 0, 0, dublin), To: time.Date(2026, 11, 30, 0, 0, 0, 0, dublin),
		Group: GroupDay, Loc: dublin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Series) != 30 || r.Series[0].Period != "2026-11-01" || r.Series[29].Period != "2026-11-30" {
		t.Fatalf("series: %d points, first %+v", len(r.Series), r.Series[0])
	}
	if r.Series[0].Upload != 1500 || r.Series[0].Download != 3500 || r.Series[1].Upload != 10 || r.Series[2].Upload != 0 {
		t.Errorf("daily = %+v %+v %+v", r.Series[0], r.Series[1], r.Series[2])
	}
	if r.Total.Upload != 1510 || r.Total.Download != 3520 || r.Total.Sessions != 3 {
		t.Errorf("total = %+v", r.Total)
	}
	if len(r.Users) != 2 || r.Users[0].Name != "alice" || r.Users[0].Sessions != 2 || r.Users[1].Name != "bob" {
		t.Fatalf("users = %+v", r.Users)
	}
	if share := r.Users[0].Share + r.Users[1].Share; share < 0.999 || share > 1.001 {
		t.Errorf("shares sum to %f", share)
	}
	if r.Users[1].OnlineSeconds != 30 { // bob: seen from connect to +30s
		t.Errorf("bob online = %d, want 30", r.Users[1].OnlineSeconds)
	}

	// One user, by month and by year.
	r, err = e.stat.Report(ctx, Query{From: time.Date(2026, 1, 1, 0, 0, 0, 0, dublin), To: time.Date(2026, 12, 31, 0, 0, 0, 0, dublin),
		Group: GroupMonth, Name: "alice", Loc: dublin})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Series) != 12 || r.Series[10].Period != "2026-11" || r.Series[10].Upload != 1010 || len(r.Users) != 1 {
		t.Errorf("monthly alice = %+v users=%+v", r.Series[10], r.Users)
	}
	r, err = e.stat.Report(ctx, Query{From: time.Date(2025, 1, 1, 0, 0, 0, 0, dublin), To: time.Date(2026, 12, 31, 0, 0, 0, 0, dublin),
		Group: GroupYear, Loc: dublin})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Series) != 2 || r.Series[1].Period != "2026" || r.Series[1].Download != 3520 {
		t.Errorf("yearly = %+v", r.Series)
	}

	// Sessions list, newest first.
	list, err := e.stat.Sessions(ctx, "alice", day1.Add(-time.Hour), day2.Add(time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || !list[0].ConnectedAt.Equal(day2) || list[0].DisconnectedAt != nil || list[1].DisconnectedAt == nil {
		t.Errorf("sessions = %+v", list)
	}
}

func TestReportValidation(t *testing.T) {
	e := newEnv(t, t0)
	d := func(m, day int) time.Time { return time.Date(2026, time.Month(m), day, 0, 0, 0, 0, time.UTC) }
	for _, q := range []Query{
		{From: d(5, 2), To: d(5, 1)},
		{From: d(1, 1), To: d(1, 2), Group: "week"},
		{From: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), To: d(1, 1)},
	} {
		if _, err := e.stat.Report(context.Background(), q); !errors.Is(err, ErrBadQuery) {
			t.Errorf("%+v: err = %v", q, err)
		}
	}
}

func TestPresets(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	cases := map[string][2]string{
		"today":        {"2026-03-15", "2026-03-15"},
		"this_month":   {"2026-03-01", "2026-03-15"},
		"last_month":   {"2026-02-01", "2026-02-28"},
		"last_30_days": {"2026-02-14", "2026-03-15"},
		"this_year":    {"2026-01-01", "2026-03-15"},
		"last_year":    {"2025-01-01", "2025-12-31"},
	}
	for name, want := range cases {
		from, to, err := Preset(name, now, time.UTC)
		if err != nil || from.Format(time.DateOnly) != want[0] || to.Format(time.DateOnly) != want[1] {
			t.Errorf("%s = %s..%s, %v; want %s..%s", name, from.Format(time.DateOnly), to.Format(time.DateOnly), err, want[0], want[1])
		}
	}
	if _, _, err := Preset("forever", now, time.UTC); !errors.Is(err, ErrBadQuery) {
		t.Errorf("unknown preset: %v", err)
	}
}

func TestReportTimeZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, t0)
	at := time.Date(2026, 11, 2, 3, 0, 10, 0, time.UTC) // 2026-11-01 22:00 in New York
	e.src.sessions = []api.Session{sess(1, "alice", at, 0, 0)}
	e.poll(at)
	e.src.sessions = []api.Session{sess(1, "alice", at, 77, 0)}
	e.poll(at.Add(30 * time.Second))

	r, err := e.stat.Report(context.Background(), Query{
		From: time.Date(2026, 11, 1, 0, 0, 0, 0, ny), To: time.Date(2026, 11, 2, 0, 0, 0, 0, ny), Loc: ny})
	if err != nil {
		t.Fatal(err)
	}
	if r.Series[0].Period != "2026-11-01" || r.Series[0].Upload != 77 || r.Series[1].Upload != 0 {
		t.Errorf("series = %+v; traffic at 03:00 UTC belongs to Nov 1 in New York", r.Series)
	}
	if r.TimeZone != "America/New_York" {
		t.Errorf("tz = %s", r.TimeZone)
	}
}
