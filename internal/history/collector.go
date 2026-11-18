// Package history records VPN sessions and hourly traffic from the agent's
// live session list, and answers statistics queries over any date range.
//
// The collector polls GET /v1/sessions (default every 30s). A session is
// identified by (client ID, connected-since). Each poll adds the byte-counter
// delta to the hour it happened in. When polls were missed (web process down,
// agent unreachable) the delta is spread over the hours of the gap and those
// hours are marked as estimated, so a restart does not show up as one spike.
// Sessions that disappear are closed at the time they were last seen.
package history

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/store"
)

// Source provides the live sessions; *agent.Client implements it.
type Source interface {
	Sessions(ctx context.Context) (api.Sessions, error)
}

// Collector polls a Source and writes history.
type Collector struct {
	DB       *store.DB
	Source   Source
	Agent    string        // agent id, "default" for now (multi-server later)
	Interval time.Duration // default 30s
	Log      *slog.Logger
	Now      func() time.Time // nil = time.Now (tests)
}

func (c *Collector) interval() time.Duration {
	if c.Interval <= 0 {
		return 30 * time.Second
	}
	return c.Interval
}

func (c *Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC().Truncate(time.Second)
	}
	return time.Now().UTC().Truncate(time.Second)
}

// Run polls until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.interval())
	defer t.Stop()
	for {
		if err := c.Poll(ctx); err != nil && ctx.Err() == nil {
			c.Log.Warn("history poll failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type activeRow struct {
	id       int64
	name     string
	in, out  int64
	lastSeen int64
}

type sessionKey struct {
	clientID    int64
	connectedAt int64
}

// Poll records one snapshot. If the agent is unreachable nothing is changed:
// open sessions stay open and the gap is spread out at the next good poll.
func (c *Collector) Poll(ctx context.Context) error {
	live, err := c.Source.Sessions(ctx)
	if err != nil {
		return err
	}
	now := c.now()

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	active, err := loadActive(ctx, tx, c.Agent)
	if err != nil {
		return err
	}

	seen := map[int64]bool{}
	for _, s := range live.Sessions {
		connected := s.ConnectedSince.Unix()
		if s.ConnectedSince.IsZero() || connected > now.Unix() {
			connected = now.Unix()
		}
		key := sessionKey{s.ClientID, connected}
		row, ok := active[key]
		if !ok {
			// New session: all its traffic so far happened since it connected.
			res, err := tx.ExecContext(ctx, `INSERT INTO sessions
				(agent, name, client_id, real_address, virtual_address, connected_at, last_seen, bytes_in, bytes_out)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (agent, client_id, connected_at) DO NOTHING`,
				c.Agent, s.Name, s.ClientID, s.RealAddress, s.VirtualAddress, connected, now.Unix(),
				s.BytesReceived, s.BytesSent)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue // already closed earlier with the same key; ignore
			}
			id, _ := res.LastInsertId()
			seen[id] = true
			if err := c.addTraffic(ctx, tx, s.Name, connected, now.Unix(), s.BytesReceived, s.BytesSent); err != nil {
				return err
			}
			continue
		}

		seen[row.id] = true
		dIn, dOut := s.BytesReceived-row.in, s.BytesSent-row.out
		if dIn < 0 || dOut < 0 { // counters reset (should not happen within a session)
			dIn, dOut = max(s.BytesReceived, 0), max(s.BytesSent, 0)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seen = ?, bytes_in = ?, bytes_out = ?,
			virtual_address = ? WHERE id = ?`,
			now.Unix(), s.BytesReceived, s.BytesSent, s.VirtualAddress, row.id); err != nil {
			return err
		}
		if err := c.addTraffic(ctx, tx, row.name, row.lastSeen, now.Unix(), dIn, dOut); err != nil {
			return err
		}
	}

	// Sessions no longer listed have ended; we only know they were alive at last_seen.
	for _, row := range active {
		if seen[row.id] {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET disconnected_at = last_seen WHERE id = ?`, row.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func loadActive(ctx context.Context, tx *sql.Tx, agent string) (map[sessionKey]activeRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, name, client_id, connected_at, bytes_in, bytes_out, last_seen
		FROM sessions WHERE agent = ? AND disconnected_at IS NULL`, agent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[sessionKey]activeRow{}
	for rows.Next() {
		var r activeRow
		var k sessionKey
		if err := rows.Scan(&r.id, &r.name, &k.clientID, &k.connectedAt, &r.in, &r.out, &r.lastSeen); err != nil {
			return nil, err
		}
		out[k] = r
	}
	return out, rows.Err()
}

// addTraffic books bytes that happened between from and to (unix seconds).
// A normal interval goes to the hour of "to"; a longer gap is spread over
// all hours it covers, proportionally to time, and marked estimated.
func (c *Collector) addTraffic(ctx context.Context, tx *sql.Tx, name string, from, to, in, out int64) error {
	if in == 0 && out == 0 {
		return nil
	}
	gap := to - from
	if gap <= int64(2*c.interval()/time.Second) || hourOf(from) == hourOf(to) {
		return addBucket(ctx, tx, c.Agent, name, hourOf(to), in, out, false)
	}

	type part struct{ hour, secs int64 }
	var parts []part
	for h := hourOf(from); h <= hourOf(to); h += 3600 {
		start, end := max(from, h), min(to, h+3600)
		if end > start {
			parts = append(parts, part{h, end - start})
		}
	}
	var doneIn, doneOut int64
	for i, p := range parts {
		pin, pout := in*p.secs/gap, out*p.secs/gap
		if i == len(parts)-1 { // remainder from integer division
			pin, pout = in-doneIn, out-doneOut
		}
		doneIn += pin
		doneOut += pout
		if err := addBucket(ctx, tx, c.Agent, name, p.hour, pin, pout, true); err != nil {
			return err
		}
	}
	return nil
}

func addBucket(ctx context.Context, tx *sql.Tx, agent, name string, hour, in, out int64, estimated bool) error {
	est := 0
	if estimated {
		est = 1
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO traffic_hourly (agent, name, hour, bytes_in, bytes_out, estimated)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (agent, name, hour) DO UPDATE SET
			bytes_in  = bytes_in  + excluded.bytes_in,
			bytes_out = bytes_out + excluded.bytes_out,
			estimated = MAX(estimated, excluded.estimated)`,
		agent, name, hour, in, out, est)
	if err != nil {
		return fmt.Errorf("add traffic: %w", err)
	}
	return nil
}

func hourOf(unix int64) int64 { return unix - unix%3600 }
