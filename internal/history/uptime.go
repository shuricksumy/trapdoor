package history

import (
	"context"
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
	out := UptimeReport{From: from, To: to, Step: step, Starts: starts, Clients: []ClientUptime{}}
	for _, c := range by {
		out.Clients = append(out.Clients, *c)
	}
	sort.Slice(out.Clients, func(i, j int) bool { return out.Clients[i].Name < out.Clients[j].Name })
	return out, nil
}
