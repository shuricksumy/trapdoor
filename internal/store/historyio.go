package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
)

// HistoryRow is one line of a server's exported history: a session ("s")
// or an hour of traffic ("h").
type HistoryRow struct {
	Kind           string `json:"t"`
	Name           string `json:"name"`
	ClientID       int64  `json:"client_id,omitempty"`
	RealAddress    string `json:"real_address,omitempty"`
	VirtualAddress string `json:"virtual_address,omitempty"`
	ConnectedAt    int64  `json:"connected_at,omitempty"`
	LastSeen       int64  `json:"last_seen,omitempty"`
	DisconnectedAt *int64 `json:"disconnected_at,omitempty"`
	Hour           int64  `json:"hour,omitempty"`
	BytesIn        int64  `json:"bytes_in"`
	BytesOut       int64  `json:"bytes_out"`
	Estimated      bool   `json:"estimated,omitempty"`
}

// ExportHistory writes the sessions and hourly traffic of one server (its
// history key) as JSON lines.
func (db *DB) ExportHistory(ctx context.Context, key string, w io.Writer) error {
	enc := json.NewEncoder(w)
	rows, err := db.QueryContext(ctx, `SELECT name, client_id, real_address, virtual_address, connected_at, last_seen,
		disconnected_at, bytes_in, bytes_out FROM sessions WHERE agent = ? ORDER BY connected_at`, key)
	if err != nil {
		return err
	}
	for rows.Next() {
		r := HistoryRow{Kind: "s"}
		var disc sql.NullInt64
		if err := rows.Scan(&r.Name, &r.ClientID, &r.RealAddress, &r.VirtualAddress, &r.ConnectedAt, &r.LastSeen, &disc, &r.BytesIn, &r.BytesOut); err != nil {
			_ = rows.Close()
			return err
		}
		if disc.Valid {
			r.DisconnectedAt = &disc.Int64
		}
		if err := enc.Encode(r); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.QueryContext(ctx, `SELECT name, hour, bytes_in, bytes_out, estimated FROM traffic_hourly WHERE agent = ? ORDER BY hour`, key)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		r := HistoryRow{Kind: "h"}
		if err := rows.Scan(&r.Name, &r.Hour, &r.BytesIn, &r.BytesOut, &r.Estimated); err != nil {
			return err
		}
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

// ImportHistory adds exported history under key; rows that are there
// already are kept. Sessions that were open at backup time are closed at
// their last poll. It returns the number of rows added.
func (db *DB) ImportHistory(ctx context.Context, key string, r io.Reader) (int, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	n := 0
	for line := 1; sc.Scan(); line++ {
		var h HistoryRow
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			return 0, fmt.Errorf("history line %d: %w", line, err)
		}
		var res sql.Result
		switch h.Kind {
		case "s":
			disc := h.LastSeen
			if h.DisconnectedAt != nil {
				disc = *h.DisconnectedAt
			}
			res, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO sessions (agent, name, client_id, real_address, virtual_address,
				connected_at, last_seen, disconnected_at, bytes_in, bytes_out) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				key, h.Name, h.ClientID, h.RealAddress, h.VirtualAddress, h.ConnectedAt, h.LastSeen, disc, h.BytesIn, h.BytesOut)
		case "h":
			res, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO traffic_hourly (agent, name, hour, bytes_in, bytes_out, estimated)
				VALUES (?, ?, ?, ?, ?, ?)`, key, h.Name, h.Hour, h.BytesIn, h.BytesOut, h.Estimated)
		default:
			return 0, fmt.Errorf("history line %d: unknown kind %q", line, h.Kind)
		}
		if err != nil {
			return 0, err
		}
		k, _ := res.RowsAffected()
		n += int(k)
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return n, tx.Commit()
}

// TrafficSince sums the traffic of every server since a time (unix
// seconds, the start of an hour): history key → [in, out].
func (db *DB) TrafficSince(ctx context.Context, since int64) (map[string][2]int64, error) {
	rows, err := db.QueryContext(ctx, `SELECT agent, SUM(bytes_in), SUM(bytes_out) FROM traffic_hourly WHERE hour >= ? GROUP BY agent`, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string][2]int64{}
	for rows.Next() {
		var k string
		var in, o int64
		if err := rows.Scan(&k, &in, &o); err != nil {
			return nil, err
		}
		out[k] = [2]int64{in, o}
	}
	return out, rows.Err()
}
