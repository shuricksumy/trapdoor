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
