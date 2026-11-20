package history

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// Step is the size of an uptime bucket.
type Step string

const (
	StepHour Step = "hour"
	StepDay  Step = "day"
)

// ClientUptime is one client's time online in a period.
type ClientUptime struct {
	Name     string     `json:"name"`
	Online   bool       `json:"online"`              // connected now
	LastSeen *time.Time `json:"last_seen,omitempty"` // the end of its latest session (now while connected); nil = never
	Sessions int        `json:"sessions"`            // sessions in the period
	// OnlineSec is the time online in the period; Buckets splits it by step.
	OnlineSec int64   `json:"online_seconds"`
	Buckets   []int64 `json:"buckets"`
}

// UptimeReport is every client's time online in [From, To), by step.
type UptimeReport struct {
	From    time.Time      `json:"from"`
	To      time.Time      `json:"to"`
	Step    Step           `json:"step"`
	Starts  []time.Time    `json:"starts"` // the start of each bucket
	Clients []ClientUptime `json:"clients"`
	// Server is how long the server was up (answered the history poll) per
	// bucket; ServerSec in all. Tracked is when that record begins (nil: not
	// yet): before it, whether the server was up is unknown.
	Server    []int64    `json:"server"`
	ServerSec int64      `json:"server_seconds"`
	Tracked   *time.Time `json:"tracked,omitempty"`
}

// Uptime reports when clients were online in [from, to). Days are days in
// loc; a session still open counts until now.
func (s *Stats) Uptime(ctx context.Context, from, to time.Time, step Step, loc *time.Location, now time.Time) (UptimeReport, error) {
	if loc == nil {
		loc = time.UTC
	}
	if !to.After(from) {
		return UptimeReport{}, fmt.Errorf("%w: to is not after from", ErrBadQuery)
	}
	var starts []time.Time
	for t := from; t.Before(to); {
		starts = append(starts, t)
		switch step {
		case StepHour:
			t = t.Add(time.Hour)
		case StepDay:
			t = t.In(loc).AddDate(0, 0, 1)
		default:
			return UptimeReport{}, fmt.Errorf("%w: step is hour or day", ErrBadQuery)
		}
		if len(starts) > 24*62 {
			return UptimeReport{}, fmt.Errorf("%w: too many buckets", ErrBadQuery)
		}
	}
	nowU := now.Unix()
	rows, err := s.DB.QueryContext(ctx, `SELECT name, connected_at, COALESCE(disconnected_at, ?), disconnected_at IS NULL
		FROM sessions WHERE agent = ? AND connected_at < ? AND COALESCE(disconnected_at, ?) > ?`,
		nowU, s.Agent, to.Unix(), nowU, from.Unix())
	if err != nil {
		return UptimeReport{}, err
	}
	defer func() { _ = rows.Close() }()
	by := map[string]*ClientUptime{}
	get := func(name string) *ClientUptime {
		c := by[name]
		if c == nil {
			c = &ClientUptime{Name: name, Buckets: make([]int64, len(starts))}
			by[name] = c
		}
		return c
	}
	bounds := make([]int64, len(starts)+1)
	for i, t := range starts {
		bounds[i] = t.Unix()
	}
	bounds[len(starts)] = to.Unix()
	for rows.Next() {
		var name string
		var a, b int64
		var open bool
		if err := rows.Scan(&name, &a, &b, &open); err != nil {
			return UptimeReport{}, err
		}
		c := get(name)
		c.Sessions++
		c.Online = c.Online || open
		a, b = max(a, bounds[0]), min(b, bounds[len(bounds)-1])
		if b <= a {
			continue
		}
		// the first bucket the session touches, then walk forward
		i := sort.Search(len(starts), func(i int) bool { return bounds[i+1] > a })
		for ; i < len(starts) && bounds[i] < b; i++ {
			lo, hi := max(a, bounds[i]), min(b, bounds[i+1])
			if hi > lo {
				c.Buckets[i] += hi - lo
				c.OnlineSec += hi - lo
			}
		}
	}
	if err := rows.Err(); err != nil {
		return UptimeReport{}, err
	}
	// When every client was last seen, from all of its history.
	last, err := s.DB.QueryContext(ctx, `SELECT name, MAX(COALESCE(disconnected_at, ?)), MAX(disconnected_at IS NULL)
		FROM sessions WHERE agent = ? GROUP BY name`, nowU, s.Agent)
	if err != nil {
		return UptimeReport{}, err
	}
	defer func() { _ = last.Close() }()
	for last.Next() {
		var name string
		var ts int64
		var open bool
		if err := last.Scan(&name, &ts, &open); err != nil {
			return UptimeReport{}, err
		}
		c := get(name)
		t := time.Unix(ts, 0).UTC()
		c.LastSeen = &t
		c.Online = c.Online || open
	}
	if err := last.Err(); err != nil {
		return UptimeReport{}, err
	}
	out := UptimeReport{From: from, To: to, Step: step, Starts: starts, Clients: []ClientUptime{}, Server: make([]int64, len(starts))}
	if err := s.serverUp(ctx, &out, bounds, nowU); err != nil {
		return UptimeReport{}, err
	}
	for _, c := range by {
		out.Clients = append(out.Clients, *c)
	}
	sort.Slice(out.Clients, func(i, j int) bool { return out.Clients[i].Name < out.Clients[j].Name })
	return out, nil
}

// upGap is how recent the last poll must be for the server to count as up
// until now (a few poll intervals).
const upGap = 90

// serverUp fills the server's time up per bucket from its spans.
func (s *Stats) serverUp(ctx context.Context, out *UptimeReport, bounds []int64, now int64) error {
	var first sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT MIN(start) FROM server_up WHERE agent = ?`, s.Agent).Scan(&first); err != nil {
		return err
	}
	if !first.Valid {
		return nil
	}
	t := time.Unix(first.Int64, 0).UTC()
	out.Tracked = &t
	lo, hi := bounds[0], bounds[len(bounds)-1]
	rows, err := s.DB.QueryContext(ctx, `SELECT start, end FROM server_up WHERE agent = ? AND start < ? AND end >= ? ORDER BY start`,
		s.Agent, hi, lo-upGap)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var a, b int64
		if err := rows.Scan(&a, &b); err != nil {
			return err
		}
		if b >= now-upGap { // polled just now: up until now
			b = now
		}
		a, b = max(a, lo), min(b, hi)
		for i := 0; i+1 < len(bounds); i++ {
			if l, h := max(a, bounds[i]), min(b, bounds[i+1]); h > l {
				out.Server[i] += h - l
				out.ServerSec += h - l
			}
		}
	}
	return rows.Err()
}

// Event is a client going up (connected) or down (disconnected).
type Event struct {
	Time   time.Time `json:"time"`
	Name   string    `json:"name"`
	Up     bool      `json:"up"`
	From   string    `json:"from,omitempty"`   // up: the address it connected from
	Lasted int64     `json:"lasted,omitempty"` // down: seconds it was up
	// Merged: quick reconnects folded into this up (with quiet set).
	Merged int `json:"merged,omitempty"`
}

// Events lists ups and downs in [from, to), newest first: offset and limit
// page through them. With quiet > 0, a down followed by an up of the same
// client within quiet is dropped (a quick reconnect), and the session counts
// as one. A session still open has no down; its up says so by having none.
func (s *Stats) Events(ctx context.Context, from, to time.Time, name string, quiet time.Duration, offset, limit int) ([]Event, int, error) {
	if !to.After(from) {
		return nil, 0, fmt.Errorf("%w: to is not after from", ErrBadQuery)
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	// Sessions touching the period, plus the quiet time before it, so a
	// reconnect right at the start is recognised.
	q := `SELECT name, connected_at, disconnected_at, real_address FROM sessions
		WHERE agent = ? AND connected_at < ? AND COALESCE(disconnected_at, last_seen) >= ?`
	args := []any{s.Agent, to.Unix(), from.Add(-quiet).Unix()}
	if name != "" {
		q += ` AND name = ?`
		args = append(args, name)
	}
	q += ` ORDER BY name, connected_at`
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	type span struct {
		name     string
		up, down int64 // down 0 = still open
		from     string
		merged   int
	}
	var spans []span
	for rows.Next() {
		var sp span
		var disc *int64
		if err := rows.Scan(&sp.name, &sp.up, &disc, &sp.from); err != nil {
			return nil, 0, err
		}
		if disc != nil {
			sp.down = *disc
		}
		// fold into the previous session of the same client when the gap is short
		if n := len(spans); quiet > 0 && n > 0 && spans[n-1].name == sp.name && spans[n-1].down != 0 &&
			sp.up-spans[n-1].down < int64(quiet/time.Second) {
			spans[n-1].down = sp.down
			spans[n-1].merged++
			continue
		}
		spans = append(spans, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var all []Event
	in := func(t int64) bool { return t >= from.Unix() && t < to.Unix() }
	for _, sp := range spans {
		if in(sp.up) {
			all = append(all, Event{Time: time.Unix(sp.up, 0).UTC(), Name: sp.name, Up: true, From: sp.from, Merged: sp.merged})
		}
		if sp.down != 0 && in(sp.down) {
			all = append(all, Event{Time: time.Unix(sp.down, 0).UTC(), Name: sp.name, Lasted: sp.down - sp.up})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Time.Equal(all[j].Time) {
			return all[i].Time.After(all[j].Time)
		}
		return !all[i].Up && all[j].Up // a down before an up at the same second, newest first
	})
	total := len(all)
	if offset >= total {
		return []Event{}, total, nil
	}
	return all[offset:min(total, offset+limit)], total, nil
}
