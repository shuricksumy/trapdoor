package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestAudit(t *testing.T) {
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	t0 := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for i, e := range []AuditEntry{
		{Username: "alex", Via: "oidc", IP: "203.0.113.5", Action: "login"},
		{Username: "alex", Via: "oidc", Action: "cert.issue", Server: "openvpn", Target: "anna"},
		{Username: "alex", Action: "cert.revoke", Server: "openvpn", Target: "bob_50%"},
		{Username: "kid", Action: "login.failed", IP: "198.51.100.9"},
		{Username: "alex", Action: "certificates.other"}, // not in the cert group
	} {
		e.Time = t0.Add(time.Duration(i) * time.Hour)
		if err := db.AddAudit(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, n, err := db.Audit(ctx, AuditFilter{}, 0, 2)
	if err != nil || n != 5 || len(all) != 2 || all[0].Action != "certificates.other" {
		t.Fatalf("all: %d %+v %v", n, all, err)
	}
	if _, n, _ := db.Audit(ctx, AuditFilter{Action: "cert"}, 0, 50); n != 2 {
		t.Errorf("group cert: %d", n)
	}
	if _, n, _ := db.Audit(ctx, AuditFilter{Action: "login"}, 0, 50); n != 2 { // login and login.failed
		t.Errorf("group login: %d", n)
	}
	if es, n, _ := db.Audit(ctx, AuditFilter{Search: "50%"}, 0, 50); n != 1 || es[0].Target != "bob_50%" {
		t.Errorf("search with %%: %d", n)
	}
	if _, n, _ := db.Audit(ctx, AuditFilter{User: "kid"}, 0, 50); n != 1 {
		t.Errorf("user: %d", n)
	}
	if _, n, _ := db.Audit(ctx, AuditFilter{From: t0.Add(time.Hour), To: t0.Add(3 * time.Hour)}, 0, 50); n != 2 {
		t.Errorf("period: %d", n)
	}
	if es, _, _ := db.Audit(ctx, AuditFilter{Search: "198.51"}, 0, 50); len(es) != 1 || es[0].Username != "kid" {
		t.Errorf("search by address: %+v", es)
	}
	if us, _ := db.AuditUsers(ctx); len(us) != 2 {
		t.Errorf("users: %v", us)
	}
}
