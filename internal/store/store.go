// Package store is the web process database (SQLite, pure Go, no cgo).
package store

import (
	"context"
	"database/sql"
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
	db := &DB{DB: sqlDB}
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
