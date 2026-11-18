package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/store"
)

// Errors; check with errors.Is.
var (
	ErrInvalidCredentials = errors.New("invalid username or password")
	ErrUserExists         = errors.New("user already exists")
	ErrUserNotFound       = errors.New("user not found")
	ErrInvalidUsername    = errors.New("username must be 1-64 letters, digits, '.', '_', '-' or '@'")
	ErrNoSession          = errors.New("not logged in")
	ErrLastUser           = errors.New("cannot delete the last admin user")
)

var validUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// User is an admin account.
type User struct {
	ID        int64     `json:"id"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
}

// Session is a logged-in browser.
type Session struct {
	User      User
	ExpiresAt time.Time
	tokenHash []byte
}

// Options configure the Service.
type Options struct {
	IdleTimeout time.Duration // default 12h
	MaxAge      time.Duration // absolute session limit, default 7 days
	Now         func() time.Time
}

// Service manages users and login sessions.
type Service struct {
	db   *store.DB
	opts Options
}

// New returns a Service backed by db.
func New(db *store.DB, opts Options) *Service {
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 12 * time.Hour
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = 7 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{db: db, opts: opts}
}

func (s *Service) now() time.Time { return s.opts.Now().UTC().Truncate(time.Second) }

// CreateUser adds an admin user.
func (s *Service) CreateUser(ctx context.Context, username, password string) (User, error) {
	if !validUsername.MatchString(username) {
		return User{}, ErrInvalidUsername
	}
	if err := CheckPassword(password); err != nil {
		return User{}, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	now := s.now().Unix()
	res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, password_changed_at)
		VALUES (?, ?, ?, ?)`, username, hash, now, now)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return User{}, fmt.Errorf("%w: %s", ErrUserExists, username)
		}
		return User{}, err
	}
	id, _ := res.LastInsertId()
	return User{ID: id, Username: username, CreatedAt: time.Unix(now, 0).UTC()}, nil
}

// SetPassword changes a user's password and logs out all their sessions.
func (s *Service) SetPassword(ctx context.Context, username, password string) error {
	if err := CheckPassword(password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", ErrUserNotFound, username)
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET password_hash = ?, password_changed_at = ? WHERE id = ?`,
		hash, s.now().Unix(), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM login_sessions WHERE user_id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteUser removes a user (and their sessions); the last user is kept.
func (s *Service) DeleteUser(ctx context.Context, username string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM users WHERE username = ?`, username)
	if err != nil {
		return err
	}
	if k, _ := res.RowsAffected(); k == 0 {
		return fmt.Errorf("%w: %s", ErrUserNotFound, username)
	}
	if n <= 1 {
		return ErrLastUser
	}
	return tx.Commit()
}

// Users lists all admin users.
func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, username, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []User{}
	for rows.Next() {
		var u User
		var created int64
		if err := rows.Scan(&u.ID, &u.Username, &created); err != nil {
			return nil, err
		}
		u.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the number of admin users.
func (s *Service) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

// Authenticate checks credentials. Unknown users take as long as wrong
// passwords, so usernames cannot be probed by timing.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	var (
		u       User
		hash    string
		created int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, username, password_hash, created_at FROM users WHERE username = ?`,
		username).Scan(&u.ID, &u.Username, &hash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		_, _ = VerifyPassword(dummyHash, password)
		return User{}, ErrInvalidCredentials
	}
	if err != nil {
		return User{}, err
	}
	ok, err := VerifyPassword(hash, password)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, ErrInvalidCredentials
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	return u, nil
}

// StartSession creates a login session and returns the cookie value.
func (s *Service) StartSession(ctx context.Context, u User, ip, userAgent string) (token string, expires time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	expires = now.Add(s.opts.MaxAge)
	if len(userAgent) > 256 {
		userAgent = userAgent[:256]
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO login_sessions (token_hash, user_id, created_at, last_used, expires_at, ip, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, hashToken(token), u.ID, now.Unix(), now.Unix(), expires.Unix(), ip, userAgent)
	if err != nil {
		return "", time.Time{}, err
	}
	// Opportunistic cleanup of expired sessions.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM login_sessions WHERE expires_at <= ? OR last_used <= ?`,
		now.Unix(), now.Add(-s.opts.IdleTimeout).Unix())
	return token, expires, nil
}

// Session resolves a cookie value; it slides the idle timeout.
func (s *Service) Session(ctx context.Context, token string) (Session, error) {
	if token == "" {
		return Session{}, ErrNoSession
	}
	h := hashToken(token)
	now := s.now()
	var (
		sess              Session
		lastUsed, expires int64
		created           int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.created_at, ls.last_used, ls.expires_at
		FROM login_sessions ls JOIN users u ON u.id = ls.user_id WHERE ls.token_hash = ?`, h).
		Scan(&sess.User.ID, &sess.User.Username, &created, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	if now.Unix() >= expires || now.Unix()-lastUsed >= int64(s.opts.IdleTimeout/time.Second) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM login_sessions WHERE token_hash = ?`, h)
		return Session{}, ErrNoSession
	}
	if now.Unix()-lastUsed >= 60 { // write at most once a minute
		_, _ = s.db.ExecContext(ctx, `UPDATE login_sessions SET last_used = ? WHERE token_hash = ?`, now.Unix(), h)
	}
	sess.User.CreatedAt = time.Unix(created, 0).UTC()
	sess.ExpiresAt = time.Unix(expires, 0).UTC()
	sess.tokenHash = h
	return sess, nil
}

// EndSession logs out one session.
func (s *Service) EndSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM login_sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
