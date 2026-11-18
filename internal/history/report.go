package history

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shuricksumy/tunnelward/internal/store"
)

// Group is the period size of a report series.
type Group string

const (
	GroupDay   Group = "day"
	GroupMonth Group = "month"
	GroupYear  Group = "year"
)

// Query selects a report. From and To are calendar dates in Loc, both
// inclusive. Name filters to one user; empty means the whole server.
type Query struct {
	From, To time.Time
	Group    Group
	Name     string
	Loc      *time.Location
}

// Traffic is upload/download from the user's point of view.
type Traffic struct {
	Upload   int64 `json:"upload"`   // bytes received by the server
	Download int64 `json:"download"` // bytes sent by the server
}

// Point is one period in a series.
type Point struct {
	Period string `json:"period"` // 2026-11-18, 2026-11 or 2026
	Traffic
	Estimated bool `json:"estimated,omitempty"`
}

// UserTotal is one user's total in the range.
type UserTotal struct {
	Name string `json:"name"`
	Traffic
	Sessions      int     `json:"sessions"`
	OnlineSeconds int64   `json:"online_seconds"`
	Share         float64 `json:"share"` // of total upload+download, 0..1
}

// Report is the answer to a Query.
type Report struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Group    Group  `json:"group"`
	TimeZone string `json:"time_zone"`
	Name     string `json:"name,omitempty"`
	Total    struct {
		Traffic
		Sessions      int   `json:"sessions"`
		OnlineSeconds int64 `json:"online_seconds"`
	} `json:"total"`
	Series []Point      `json:"series"`
	Users  []UserTotal  `json:"users"`
	Rows   []PeriodUser `json:"-"` // per period and user, for CSV
}

// PeriodUser is one user's traffic in one period.
type PeriodUser struct {
	Period string
	Name   string
	Traffic
	Estimated bool
}

// Stats answers queries over the recorded history.
type Stats struct {
	DB    *store.DB
	Agent string
}

// MaxRange limits a query to keep it cheap.
const MaxRange = 10 * 366 * 24 * time.Hour

// Report builds the report for q.
func (s *Stats) Report(ctx context.Context, q Query) (Report, error) {
	if q.Loc == nil {
		q.Loc = time.UTC
	}
	if q.Group == "" {
		q.Group = GroupDay
	}
	if q.Group != GroupDay && q.Group != GroupMonth && q.Group != GroupYear {
		return Report{}, fmt.Errorf("%w: group must be day, month or year", ErrBadQuery)
	}
	start := midnight(q.From, q.Loc)
	end := midnight(q.To, q.Loc).AddDate(0, 0, 1) // exclusive
	if !end.After(start) {
		return Report{}, fmt.Errorf("%w: to is before from", ErrBadQuery)
	}
	if end.Sub(start) > MaxRange {
		return Report{}, fmt.Errorf("%w: range longer than 10 years", ErrBadQuery)
	}

	r := Report{
		From: start.Format(time.DateOnly), To: end.AddDate(0, 0, -1).Format(time.DateOnly),
		Group: q.Group, TimeZone: q.Loc.String(), Name: q.Name,
		Series: []Point{}, Users: []UserTotal{},
	}

	// Pre-fill every period so charts have no holes.
	index := map[string]int{}
	for p := truncate(start, q.Group); p.Before(end); p = next(p, q.Group) {
		key := label(p, q.Group)
		index[key] = len(r.Series)
		r.Series = append(r.Series, Point{Period: key})
	}

	users := map[string]*UserTotal{}
	user := func(name string) *UserTotal {
		u, ok := users[name]
		if !ok {
			u = &UserTotal{Name: name}
			users[name] = u
		}
		return u
	}

	// Traffic. Hours are UTC; a local day boundary always falls on a UTC hour
	// except in half-hour time zones, where up to 30 minutes shift days.
	sqlq := `SELECT name, hour, bytes_in, bytes_out, estimated FROM traffic_hourly
		WHERE agent = ? AND hour >= ? AND hour < ?`
	args := []any{s.Agent, start.Unix(), end.Unix()}
	if q.Name != "" {
		sqlq += ` AND name = ?`
		args = append(args, q.Name)
	}
	rows, err := s.DB.QueryContext(ctx, sqlq+` ORDER BY hour`, args...)
	if err != nil {
		return Report{}, err
	}
	perPeriodUser := map[[2]string]*PeriodUser{}
	for rows.Next() {
		var (
			name          string
			hour, in, out int64
			est           bool
		)
		if err := rows.Scan(&name, &hour, &in, &out, &est); err != nil {
			_ = rows.Close()
			return Report{}, err
		}
		key := label(time.Unix(hour, 0).In(q.Loc), q.Group)
		i, ok := index[key]
		if !ok {
			continue
		}
		pt := &r.Series[i]
		pt.Upload += in
		pt.Download += out
		pt.Estimated = pt.Estimated || est
		u := user(name)
		u.Upload += in
		u.Download += out

		pu := perPeriodUser[[2]string{key, name}]
		if pu == nil {
			pu = &PeriodUser{Period: key, Name: name}
			perPeriodUser[[2]string{key, name}] = pu
		}
		pu.Upload += in
		pu.Download += out
		pu.Estimated = pu.Estimated || est
	}
	if err := rows.Close(); err != nil {
		return Report{}, err
	}
	if err := rows.Err(); err != nil {
		return Report{}, err
	}

	// Sessions overlapping the range: count those that started in it, and
	// online time clipped to it.
	sqlq = `SELECT name, connected_at, COALESCE(disconnected_at, last_seen) FROM sessions
		WHERE agent = ? AND connected_at < ? AND COALESCE(disconnected_at, last_seen) >= ?`
	args = []any{s.Agent, end.Unix(), start.Unix()}
	if q.Name != "" {
		sqlq += ` AND name = ?`
		args = append(args, q.Name)
	}
	rows, err = s.DB.QueryContext(ctx, sqlq, args...)
	if err != nil {
		return Report{}, err
	}
	for rows.Next() {
		var (
			name     string
			from, to int64
		)
		if err := rows.Scan(&name, &from, &to); err != nil {
			_ = rows.Close()
			return Report{}, err
		}
		u := user(name)
		if from >= start.Unix() {
			u.Sessions++
		}
		u.OnlineSeconds += min(to, end.Unix()) - max(from, start.Unix())
	}
	if err := rows.Close(); err != nil {
		return Report{}, err
	}

	for _, u := range users {
		r.Total.Upload += u.Upload
		r.Total.Download += u.Download
		r.Total.Sessions += u.Sessions
		r.Total.OnlineSeconds += u.OnlineSeconds
	}
	total := r.Total.Upload + r.Total.Download
	for _, u := range users {
		if total > 0 {
			u.Share = float64(u.Upload+u.Download) / float64(total)
		}
		r.Users = append(r.Users, *u)
	}
	sort.Slice(r.Users, func(i, j int) bool {
		a, b := r.Users[i], r.Users[j]
		if a.Upload+a.Download != b.Upload+b.Download {
			return a.Upload+a.Download > b.Upload+b.Download
		}
		return a.Name < b.Name
	})
	for _, pu := range perPeriodUser {
		r.Rows = append(r.Rows, *pu)
	}
	sort.Slice(r.Rows, func(i, j int) bool {
		if r.Rows[i].Period != r.Rows[j].Period {
			return r.Rows[i].Period < r.Rows[j].Period
		}
		return r.Rows[i].Name < r.Rows[j].Name
	})
	return r, nil
}

// Session is one recorded session.
type Session struct {
	Name           string     `json:"name"`
	RealAddress    string     `json:"real_address"`
	VirtualAddress string     `json:"virtual_address,omitempty"`
	ConnectedAt    time.Time  `json:"connected_at"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"` // nil while connected
	LastSeen       time.Time  `json:"last_seen"`
	DurationSec    int64      `json:"duration_seconds"`
	Traffic
}

// Sessions lists recorded sessions that overlap [from, to) newest first.
func (s *Stats) Sessions(ctx context.Context, name string, from, to time.Time, limit int) ([]Session, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	q := `SELECT name, real_address, virtual_address, connected_at, disconnected_at, last_seen, bytes_in, bytes_out
		FROM sessions WHERE agent = ? AND connected_at < ? AND COALESCE(disconnected_at, last_seen) >= ?`
	args := []any{s.Agent, to.Unix(), from.Unix()}
	if name != "" {
		q += ` AND name = ?`
		args = append(args, name)
	}
	q += ` ORDER BY connected_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []Session{}
	for rows.Next() {
		var (
			x          Session
			conn, seen int64
			disc       *int64
		)
		if err := rows.Scan(&x.Name, &x.RealAddress, &x.VirtualAddress, &conn, &disc, &seen, &x.Upload, &x.Download); err != nil {
			return nil, err
		}
		x.ConnectedAt = time.Unix(conn, 0).UTC()
		x.LastSeen = time.Unix(seen, 0).UTC()
		end := seen
		if disc != nil {
			t := time.Unix(*disc, 0).UTC()
			x.DisconnectedAt = &t
			end = *disc
		}
		x.DurationSec = end - conn
		out = append(out, x)
	}
	return out, rows.Err()
}

// ErrBadQuery marks invalid report parameters.
var ErrBadQuery = errors.New("bad query")

func midnight(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func truncate(t time.Time, g Group) time.Time {
	y, m, _ := t.Date()
	switch g {
	case GroupYear:
		return time.Date(y, 1, 1, 0, 0, 0, 0, t.Location())
	case GroupMonth:
		return time.Date(y, m, 1, 0, 0, 0, 0, t.Location())
	default:
		return midnight(t, t.Location())
	}
}

func next(t time.Time, g Group) time.Time {
	switch g {
	case GroupYear:
		return t.AddDate(1, 0, 0)
	case GroupMonth:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

func label(t time.Time, g Group) string {
	switch g {
	case GroupYear:
		return t.Format("2006")
	case GroupMonth:
		return t.Format("2006-01")
	default:
		return t.Format(time.DateOnly)
	}
}

// Preset resolves a named range relative to now in loc.
func Preset(name string, now time.Time, loc *time.Location) (from, to time.Time, err error) {
	now = now.In(loc)
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, loc)
	switch name {
	case "today":
		return today, today, nil
	case "this_month":
		return time.Date(y, m, 1, 0, 0, 0, 0, loc), today, nil
	case "last_month":
		first := time.Date(y, m, 1, 0, 0, 0, 0, loc)
		return first.AddDate(0, -1, 0), first.AddDate(0, 0, -1), nil
	case "last_30_days":
		return today.AddDate(0, 0, -29), today, nil
	case "this_year":
		return time.Date(y, 1, 1, 0, 0, 0, 0, loc), today, nil
	case "last_year":
		return time.Date(y-1, 1, 1, 0, 0, 0, 0, loc), time.Date(y-1, 12, 31, 0, 0, 0, 0, loc), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("%w: unknown preset %q", ErrBadQuery, name)
	}
}
