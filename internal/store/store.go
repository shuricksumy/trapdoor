// Package store is the web process database (SQLite, pure Go, no cgo).
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver" // database/sql driver "sqlite3"
	_ "github.com/ncruces/go-sqlite3/embed"  // bundled SQLite build
)

// DB wraps the database handle.
type DB struct {
	*sql.DB
	path string
}

// Open opens (creating if needed) the database file at path and applies
// migrations.
func Open(ctx context.Context, path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=journal_mode(wal)&_txlock=immediate"
	sqlDB, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db := &DB{DB: sqlDB, path: path}
	// A new file switched to WAL by two processes at once (web starting
	// while "trapdoor user add" runs) answers "database is locked" without
	// waiting for busy_timeout: try again for a few seconds.
	for try := 1; ; try++ {
		err = db.migrate(ctx)
		if err == nil || try == 20 || !strings.Contains(err.Error(), "database is locked") {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(try) * 25 * time.Millisecond):
		}
	}
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// migrations are applied in order; never edit one that has been released.
var migrations = []string{
	// 1: session history and hourly traffic
	`CREATE TABLE sessions (
		id              INTEGER PRIMARY KEY,
		agent           TEXT    NOT NULL,
		name            TEXT    NOT NULL,
		client_id       INTEGER NOT NULL,
		real_address    TEXT    NOT NULL,
		virtual_address TEXT    NOT NULL DEFAULT '',
		connected_at    INTEGER NOT NULL, -- unix seconds
		last_seen       INTEGER NOT NULL,
		disconnected_at INTEGER,          -- NULL while connected
		bytes_in        INTEGER NOT NULL DEFAULT 0, -- received by the server (user upload)
		bytes_out       INTEGER NOT NULL DEFAULT 0, -- sent by the server (user download)
		UNIQUE (agent, client_id, connected_at)
	);
	CREATE INDEX sessions_active ON sessions (agent, disconnected_at);
	CREATE INDEX sessions_name_time ON sessions (name, connected_at);

	CREATE TABLE traffic_hourly (
		agent     TEXT    NOT NULL,
		name      TEXT    NOT NULL,
		hour      INTEGER NOT NULL, -- unix seconds, start of the UTC hour
		bytes_in  INTEGER NOT NULL DEFAULT 0,
		bytes_out INTEGER NOT NULL DEFAULT 0,
		estimated INTEGER NOT NULL DEFAULT 0, -- 1 if spread over a gap in polling
		PRIMARY KEY (agent, name, hour)
	) WITHOUT ROWID;
	CREATE INDEX traffic_hour ON traffic_hourly (agent, hour);`,

	// 2: admin users and login sessions
	`CREATE TABLE users (
		id            INTEGER PRIMARY KEY,
		username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
		password_hash TEXT    NOT NULL,
		created_at    INTEGER NOT NULL,
		password_changed_at INTEGER NOT NULL
	);
	CREATE TABLE login_sessions (
		token_hash  BLOB    PRIMARY KEY, -- sha256 of the cookie value; the value itself is never stored
		user_id     INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
		created_at  INTEGER NOT NULL,
		last_used   INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,   -- absolute limit
		ip          TEXT    NOT NULL DEFAULT '',
		user_agent  TEXT    NOT NULL DEFAULT ''
	) WITHOUT ROWID;
	CREATE INDEX login_sessions_user ON login_sessions (user_id);`,

	// 3: history is keyed by server id; the single server so far is "openvpn"
	`UPDATE sessions SET agent = 'openvpn' WHERE agent = 'default';
	UPDATE traffic_hourly SET agent = 'openvpn' WHERE agent = 'default';`,

	// 4: other hosts with an agent (their servers' history is keyed "<host>.<id>")
	`CREATE TABLE hosts (
		name        TEXT    PRIMARY KEY,
		url         TEXT    NOT NULL,
		token       TEXT    NOT NULL,
		fingerprint TEXT    NOT NULL,
		created_at  INTEGER NOT NULL
	) WITHOUT ROWID;`,

	// 5: one-time tokens nodes join with (only a hash is kept)
	`CREATE TABLE join_tokens (
		token_hash  BLOB    PRIMARY KEY,
		name        TEXT    NOT NULL DEFAULT '', -- the node it is for; '' = any
		created_at  INTEGER NOT NULL,
		expires_at  INTEGER NOT NULL,
		used_at     INTEGER
	) WITHOUT ROWID;`,

	// 6: settings changed in the UI (JSON values), e.g. the backup schedule
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	) WITHOUT ROWID;`,

	// 7: clients' email addresses (to send them their profile)
	`CREATE TABLE client_emails (
		agent  TEXT NOT NULL,
		client TEXT NOT NULL,
		email  TEXT NOT NULL,
		PRIMARY KEY (agent, client)
	) WITHOUT ROWID;`,

	// 8: OpenVPN reports a connection that has not authenticated yet (or
	// failed to) as "UNDEF"; such rows were stored as sessions before.
	`DELETE FROM sessions WHERE name IN ('UNDEF', '');
	DELETE FROM traffic_hourly WHERE name IN ('UNDEF', '');`,

	// 9: which clients' online / offline changes are sent, and how
	`CREATE TABLE IF NOT EXISTS client_notify (
		agent    TEXT    NOT NULL,
		client   TEXT    NOT NULL,
		email    INTEGER NOT NULL DEFAULT 0,
		telegram INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (agent, client)
	) WITHOUT ROWID;`,
}

func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&v)
	if err != nil {
		return err
	}
	if v > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this trapdoor (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := db.BeginTx(ctx, nil) // IMMEDIATE (_txlock): one process at a time
		if err != nil {
			return err
		}
		// Another process (e.g. "trapdoor user add" while web starts) may
		// have applied it since the version was read.
		var done int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&done); err != nil {
			_ = tx.Rollback()
			return err
		}
		if done > i {
			_ = tx.Rollback()
			continue
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Host is another host's agent, as stored.
type Host struct {
	Name, URL, Token, Fingerprint string
}

// Hosts lists the hosts by name.
func (db *DB) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := db.QueryContext(ctx, `SELECT name, url, token, fingerprint FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Host
	for rows.Next() {
		var h Host
		if err := rows.Scan(&h.Name, &h.URL, &h.Token, &h.Fingerprint); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SaveHost adds a host or replaces the one with the same name.
func (db *DB) SaveHost(ctx context.Context, h Host, now int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO hosts (name, url, token, fingerprint, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET url = excluded.url, token = excluded.token, fingerprint = excluded.fingerprint`,
		h.Name, h.URL, h.Token, h.Fingerprint, now)
	return err
}

// RemoveHost deletes a host; its servers' history stays.
func (db *DB) RemoveHost(ctx context.Context, name string) (bool, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM hosts WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Snapshot writes a consistent copy of the database to a new file next to
// it (the web container's root is read-only) and returns its path; the
// caller removes it.
func (db *DB) Snapshot(ctx context.Context) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(db.path), ".snapshot-*.db")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	_ = f.Close()
	_ = os.Remove(tmp) // VACUUM INTO wants a new file
	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// CreateJoinToken keeps a new join token's hash.
func (db *DB) CreateJoinToken(ctx context.Context, hash []byte, name string, now, expires int64) error {
	_, err := db.ExecContext(ctx, `INSERT INTO join_tokens (token_hash, name, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		hash, name, now, expires)
	return err
}

// JoinTokenFor reports whether hash is an unused, unexpired token for a
// node called name (or for any name).
func (db *DB) JoinTokenFor(ctx context.Context, hash []byte, name string, now int64) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM join_tokens WHERE token_hash = ? AND used_at IS NULL
		AND expires_at > ? AND (name = '' OR name = ?)`, hash, now, name).Scan(&n)
	return n > 0, err
}

// UseJoinToken marks a token used; false when it was used meanwhile.
func (db *DB) UseJoinToken(ctx context.Context, hash []byte, now int64) (bool, error) {
	res, err := db.ExecContext(ctx, `UPDATE join_tokens SET used_at = ? WHERE token_hash = ? AND used_at IS NULL`, now, hash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Setting reads the JSON setting key into v; false when it is not set.
func (db *DB) Setting(ctx context.Context, key string, v any) (bool, error) {
	var raw string
	err := db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetSetting stores v as JSON under key.
func (db *DB) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(b))
	return err
}

// ClientEmails are the email addresses of a server's clients (client → email).
func (db *DB) ClientEmails(ctx context.Context, agent string) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT client, email FROM client_emails WHERE agent = ?`, agent)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]string{}
	for rows.Next() {
		var c, e string
		if err := rows.Scan(&c, &e); err != nil {
			return nil, err
		}
		out[c] = e
	}
	return out, rows.Err()
}

// SetClientEmail stores a client's email address; empty removes it.
func (db *DB) SetClientEmail(ctx context.Context, agent, client, email string) error {
	if email == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM client_emails WHERE agent = ? AND client = ?`, agent, client)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO client_emails (agent, client, email) VALUES (?, ?, ?)
		ON CONFLICT (agent, client) DO UPDATE SET email = excluded.email`, agent, client, email)
	return err
}

// NotifyFlags say how a client's online / offline changes are sent.
type NotifyFlags struct {
	Email    bool `json:"email"`
	Telegram bool `json:"telegram"`
}

// ClientNotify returns the clients with notifications on, by server id and
// client name; agent "" returns every server.
func (db *DB) ClientNotify(ctx context.Context, agent string) (map[string]map[string]NotifyFlags, error) {
	q := `SELECT agent, client, email, telegram FROM client_notify`
	args := []any{}
	if agent != "" {
		q += ` WHERE agent = ?`
		args = append(args, agent)
	}
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]map[string]NotifyFlags{}
	for rows.Next() {
		var a, c string
		var f NotifyFlags
		if err := rows.Scan(&a, &c, &f.Email, &f.Telegram); err != nil {
			return nil, err
		}
		if out[a] == nil {
			out[a] = map[string]NotifyFlags{}
		}
		out[a][c] = f
	}
	return out, rows.Err()
}

// SetClientNotify stores how a client's changes are sent; all off removes it.
func (db *DB) SetClientNotify(ctx context.Context, agent, client string, f NotifyFlags) error {
	if !f.Email && !f.Telegram {
		_, err := db.ExecContext(ctx, `DELETE FROM client_notify WHERE agent = ? AND client = ?`, agent, client)
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO client_notify (agent, client, email, telegram) VALUES (?, ?, ?, ?)
		ON CONFLICT (agent, client) DO UPDATE SET email = excluded.email, telegram = excluded.telegram`, agent, client, f.Email, f.Telegram)
	return err
}

// Presence is a client with an open session (connected now, as far as the
// history knows).
type Presence struct {
	Agent, Name string
	Since       time.Time
	From        string // real address
}

// Online lists the clients with an open session, one row per client (the
// latest session).
func (db *DB) Online(ctx context.Context) ([]Presence, error) {
	rows, err := db.QueryContext(ctx, `SELECT agent, name, MAX(connected_at), real_address
		FROM sessions WHERE disconnected_at IS NULL GROUP BY agent, name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Presence
	for rows.Next() {
		var p Presence
		var since int64
		if err := rows.Scan(&p.Agent, &p.Name, &since, &p.From); err != nil {
			return nil, err
		}
		p.Since = time.Unix(since, 0).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastDisconnect is when a client's latest closed session ended (zero if none).
func (db *DB) LastDisconnect(ctx context.Context, agent, name string) (time.Time, error) {
	var ts sql.NullInt64
	err := db.QueryRowContext(ctx, `SELECT MAX(disconnected_at) FROM sessions WHERE agent = ? AND name = ?`, agent, name).Scan(&ts)
	if err != nil || !ts.Valid {
		return time.Time{}, err
	}
	return time.Unix(ts.Int64, 0).UTC(), nil
}
