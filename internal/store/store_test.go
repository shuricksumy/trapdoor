package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "td.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	var v int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil || v != len(migrations) {
		t.Fatalf("version = %d, %v", v, err)
	}
	if _, err := db.Exec(`INSERT INTO traffic_hourly (agent, name, hour) VALUES ('a', 'b', 0)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("db mode = %v", fi.Mode().Perm())
	}
	// Re-open: migrations are not applied twice, data survives.
	db, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM traffic_hourly`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d, %v", n, err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "td.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (999)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(context.Background(), path); err == nil {
		t.Error("expected error for newer schema")
	}
}

// Two processes opening a new database at once ("trapdoor user add" while
// web starts) must not both run the migrations.
func TestOpenConcurrently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "td.db")
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			db, err := Open(context.Background(), path)
			if err == nil {
				err = db.Close()
			}
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

func TestMigrationDropsUndefSessions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "td.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// A database from before migration 8, with an unauthenticated connection
	// stored as client "UNDEF" next to a real one.
	for i, name := range []string{"UNDEF", "alice"} {
		if _, err := db.ExecContext(ctx, `INSERT INTO sessions (agent, name, client_id, real_address, connected_at, last_seen)
			VALUES ('openvpn', ?, ?, '203.0.113.9:1', 1, 1)`, name, i+1); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO traffic_hourly (agent, name, hour) VALUES ('openvpn', ?, 0)`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE schema_version SET version = 7`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	if db, err = Open(ctx, path); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, table := range []string{"sessions", "traffic_hourly"} {
		var undef, alice int
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE name = 'UNDEF'`).Scan(&undef)
		_ = db.QueryRowContext(ctx, `SELECT count(*) FROM `+table+` WHERE name = 'alice'`).Scan(&alice)
		if undef != 0 || alice != 1 {
			t.Errorf("%s: UNDEF %d, alice %d", table, undef, alice)
		}
	}
}

func TestUsage(t *testing.T) {
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for _, r := range []struct {
		agent, name   string
		hour, in, out int64
	}{
		{"openvpn", "anna", 3600, 100, 1000},
		{"openvpn", "anna", 7200, 5, 50},
		{"openvpn", "bob", 0, 1, 1}, // before
		{"wg0", "anna", 7200, 9, 9}, // other server
	} {
		if _, err := db.Exec(`INSERT INTO traffic_hourly (agent, name, hour, bytes_in, bytes_out) VALUES (?, ?, ?, ?, ?)`, r.agent, r.name, r.hour, r.in, r.out); err != nil {
			t.Fatal(err)
		}
	}
	u, err := db.Usage(context.Background(), "openvpn", 3600)
	if err != nil || len(u) != 1 || u["anna"] != 1155 {
		t.Errorf("usage = %v %v", u, err)
	}
}
