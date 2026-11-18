package auth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/store"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newService(t *testing.T) (*Service, *clock) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	c := &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)}
	return New(db, Options{IdleTimeout: time.Hour, MaxAge: 24 * time.Hour, Now: c.Now}), c
}

var ctx = context.Background()

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "pbkdf2-sha256$600000$") {
		t.Errorf("hash = %s", h)
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("same hash twice: salt missing")
	}
	if ok, err := VerifyPassword(h, "correct horse battery"); !ok || err != nil {
		t.Errorf("verify good: %v %v", ok, err)
	}
	if ok, _ := VerifyPassword(h, "wrong horse battery"); ok {
		t.Error("wrong password verified")
	}
	if _, err := VerifyPassword("md5$abc", "x"); err == nil {
		t.Error("unknown format accepted")
	}
	for _, pw := range []string{"short", strings.Repeat("x", 257)} {
		if CheckPassword(pw) == nil {
			t.Errorf("policy accepted %d chars", len(pw))
		}
	}
}

func TestUsers(t *testing.T) {
	s, _ := newService(t)
	if _, err := s.CreateUser(ctx, "admin", "long-enough-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser(ctx, "ADMIN", "long-enough-pw"); !errors.Is(err, ErrUserExists) {
		t.Errorf("case-insensitive duplicate: %v", err)
	}
	if _, err := s.CreateUser(ctx, "bad name", "long-enough-pw"); !errors.Is(err, ErrInvalidUsername) {
		t.Errorf("bad name: %v", err)
	}
	if _, err := s.CreateUser(ctx, "bob", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Errorf("weak: %v", err)
	}
	if _, err := s.Authenticate(ctx, "Admin", "long-enough-pw"); err != nil {
		t.Errorf("auth (case-insensitive username): %v", err)
	}
	if _, err := s.Authenticate(ctx, "admin", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("wrong pw: %v", err)
	}
	if _, err := s.Authenticate(ctx, "nobody", "whatever-pw"); !errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("unknown user: %v", err)
	}
	if err := s.DeleteUser(ctx, "admin"); !errors.Is(err, ErrLastUser) {
		t.Errorf("delete last: %v", err)
	}
	if _, err := s.CreateUser(ctx, "bob", "long-enough-pw"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(ctx, "bob"); err != nil {
		t.Errorf("delete bob: %v", err)
	}
	if err := s.DeleteUser(ctx, "bob"); !errors.Is(err, ErrUserNotFound) {
		t.Errorf("delete again: %v", err)
	}
	if us, _ := s.Users(ctx); len(us) != 1 || us[0].Username != "admin" {
		t.Errorf("users = %+v", us)
	}
}

func TestSessions(t *testing.T) {
	s, c := newService(t)
	u, _ := s.CreateUser(ctx, "admin", "long-enough-pw")
	tok, exp, err := s.StartSession(ctx, u, "1.2.3.4", "test")
	if err != nil || len(tok) < 40 || !exp.Equal(c.Now().Add(24*time.Hour)) {
		t.Fatalf("start: %q %v %v", tok, exp, err)
	}
	var stored []byte
	_ = s.db.QueryRow(`SELECT token_hash FROM login_sessions`).Scan(&stored)
	if string(stored) == tok || len(stored) != 32 {
		t.Error("token must be stored hashed")
	}

	if sess, err := s.Session(ctx, tok); err != nil || sess.User.Username != "admin" {
		t.Fatalf("session: %+v %v", sess, err)
	}
	if _, err := s.Session(ctx, "forged"); !errors.Is(err, ErrNoSession) {
		t.Errorf("forged: %v", err)
	}

	// Activity keeps it alive past the idle timeout...
	for range 3 {
		c.Add(50 * time.Minute)
		if _, err := s.Session(ctx, tok); err != nil {
			t.Fatalf("active session expired: %v", err)
		}
	}
	// ...idle does not.
	c.Add(61 * time.Minute)
	if _, err := s.Session(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Errorf("idle session still valid: %v", err)
	}

	// Absolute limit even when active.
	tok, _, _ = s.StartSession(ctx, u, "", "")
	for range 30 {
		c.Add(50 * time.Minute)
		_, err = s.Session(ctx, tok)
	}
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("session older than MaxAge still valid")
	}

	// Logout and password change end sessions.
	tok, _, _ = s.StartSession(ctx, u, "", "")
	if err := s.EndSession(ctx, tok); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Error("session valid after logout")
	}
	tok, _, _ = s.StartSession(ctx, u, "", "")
	if err := s.SetPassword(ctx, "admin", "another-long-pw"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Session(ctx, tok); !errors.Is(err, ErrNoSession) {
		t.Error("session valid after password change")
	}
	if _, err := s.Authenticate(ctx, "admin", "another-long-pw"); err != nil {
		t.Errorf("new password: %v", err)
	}
}

func TestLimiter(t *testing.T) {
	c := &clock{t: time.Now()}
	l := &Limiter{MaxFailures: 3, MaxUserFailures: 5, Window: time.Minute, Now: c.Now}
	for range 3 {
		if ok, _ := l.Allowed("1.1.1.1", "admin"); !ok {
			t.Fatal("blocked too early")
		}
		l.Fail("1.1.1.1", "admin")
	}
	if ok, wait := l.Allowed("1.1.1.1", "admin"); ok || wait <= 0 {
		t.Error("IP not blocked after 3 failures")
	}
	if ok, _ := l.Allowed("2.2.2.2", "admin"); !ok {
		t.Error("other IP blocked by per-IP limit")
	}
	l.Fail("2.2.2.2", "admin")
	l.Fail("3.3.3.3", "admin")
	if ok, _ := l.Allowed("4.4.4.4", "admin"); ok {
		t.Error("username not blocked after 5 failures from many IPs")
	}
	c.Add(61 * time.Second)
	if ok, _ := l.Allowed("1.1.1.1", "admin"); !ok {
		t.Error("still blocked after window")
	}
}
