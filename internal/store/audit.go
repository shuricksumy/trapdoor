package store

import (
	"context"
	"strings"
	"time"
)

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID       int64     `json:"id"`
	Time     time.Time `json:"time"`
	Username string    `json:"username"`
	Via      string    `json:"via,omitempty"`
	IP       string    `json:"ip,omitempty"`
	Action   string    `json:"action"`
	Server   string    `json:"server,omitempty"`
	Target   string    `json:"target,omitempty"`
}

// AuditFilter narrows the audit log; empty fields match everything.
type AuditFilter struct {
	From, To time.Time // To is exclusive
	User     string
	Action   string // an action or its group: "cert" matches cert.issue, cert.revoke, …
	Search   string // in the target, action, user and address
}

// AuditKeep is how long the audit log is kept.
const AuditKeep = 2 * 365 * 24 * time.Hour

// AddAudit writes a line; now and then lines older than AuditKeep go.
func (db *DB) AddAudit(ctx context.Context, e AuditEntry) error {
	res, err := db.ExecContext(ctx, `INSERT INTO audit (time, username, via, ip, action, server, target) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.Time.Unix(), e.Username, e.Via, e.IP, e.Action, e.Server, e.Target)
	if err != nil {
		return err
	}
	if id, _ := res.LastInsertId(); id%500 == 0 {
		_, _ = db.ExecContext(ctx, `DELETE FROM audit WHERE time < ?`, e.Time.Add(-AuditKeep).Unix())
	}
	return nil
}

func (f AuditFilter) where() (string, []any) {
	var conds []string
	var args []any
	if !f.From.IsZero() {
		conds, args = append(conds, "time >= ?"), append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		conds, args = append(conds, "time < ?"), append(args, f.To.Unix())
	}
	if f.User != "" {
		conds, args = append(conds, "username = ?"), append(args, f.User)
	}
	if f.Action != "" {
		conds, args = append(conds, "(action = ? OR action LIKE ? ESCAPE '\\')"), append(args, f.Action, likeEscape(f.Action)+".%")
	}
	if f.Search != "" {
		q := "%" + likeEscape(f.Search) + "%"
		conds = append(conds, "(target LIKE ? ESCAPE '\\' OR action LIKE ? ESCAPE '\\' OR username LIKE ? ESCAPE '\\' OR ip LIKE ? ESCAPE '\\' OR server LIKE ? ESCAPE '\\')")
		args = append(args, q, q, q, q, q)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// Audit returns a page of matching lines, newest first, and how many match.
func (db *DB) Audit(ctx context.Context, f AuditFilter, offset, limit int) ([]AuditEntry, int, error) {
	where, args := f.where()
	var total int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, time, username, via, ip, action, server, target FROM audit`+where+
		` ORDER BY time DESC, id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Username, &e.Via, &e.IP, &e.Action, &e.Server, &e.Target); err != nil {
			return nil, 0, err
		}
		e.Time = time.Unix(t, 0).UTC()
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// AuditUsers are the names in the audit log (for the filter).
func (db *DB) AuditUsers(ctx context.Context) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT username FROM audit WHERE username != '' ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}
