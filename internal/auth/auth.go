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
	// ErrLastAdmin: the change would leave no enabled admin.
	ErrLastAdmin = errors.New("there must be at least one enabled admin")
	ErrLastUser  = ErrLastAdmin // the older name
	ErrBadRole   = errors.New("role: admin or viewer")
)

// Roles. A viewer sees everything an admin sees but changes nothing.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// Sources: a local account (password) or one made by signing in with OIDC.
const (
	SourceLocal = "local"
	SourceOIDC  = "oidc"
)

var validUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// User is an account: an admin or a viewer.
type User struct {
	ID          int64     `json:"id"`
	Username    string    `json:"username"`
	CreatedAt   time.Time `json:"created_at"`
	Role        string    `json:"role"`
	Source      string    `json:"source"`
	HasPassword bool      `json:"has_password"`
	Disabled    bool      `json:"disabled"`
	LastLoginAt time.Time `json:"last_login_at,omitzero"`
	LastLoginIP string    `json:"last_login_ip,omitempty"`
	// LastLoginVia: "password" or "oidc".
	LastLoginVia string `json:"last_login_via,omitempty"`
}

// Session is a logged-in browser.
type Session struct {
	User      User
	Via       string // how it signed in: password or oidc
	ExpiresAt time.Time
	tokenHash []byte
}

const userCols = `id, username, created_at, role, source, password_hash != '', disabled, last_login_at, last_login_ip, last_login_via`

type scanner interface{ Scan(...any) error }

func scanUser(r scanner) (User, error) {
	var u User
	var created, last int64
	err := r.Scan(&u.ID, &u.Username, &created, &u.Role, &u.Source, &u.HasPassword, &u.Disabled, &last, &u.LastLoginIP, &u.LastLoginVia)
	u.CreatedAt = time.Unix(created, 0).UTC()
	if last > 0 {
		u.LastLoginAt = time.Unix(last, 0).UTC()
	}
	return u, err
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
	return User{ID: id, Username: username, CreatedAt: time.Unix(now, 0).UTC(), Role: RoleAdmin, Source: SourceLocal, HasPassword: true}, nil
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

// DeleteUser removes a user (and their sessions); the last enabled admin is kept.
func (s *Service) DeleteUser(ctx context.Context, username string) error {
	return s.changeUser(ctx, username, func(tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		return err
	})
}

// SetRole makes a user an admin or a viewer.
func (s *Service) SetRole(ctx context.Context, username, role string) error {
	if role != RoleAdmin && role != RoleViewer {
		return ErrBadRole
	}
	return s.changeUser(ctx, username, func(tx *sql.Tx, id int64) error {
		_, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, id)
		return err
	})
}

// SetDisabled turns a user off (ending their sessions) or on again.
func (s *Service) SetDisabled(ctx context.Context, username string, disabled bool) error {
	return s.changeUser(ctx, username, func(tx *sql.Tx, id int64) error {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET disabled = ? WHERE id = ?`, disabled, id); err != nil {
			return err
		}
		if disabled {
			_, err := tx.ExecContext(ctx, `DELETE FROM login_sessions WHERE user_id = ?`, id)
			return err
		}
		return nil
	})
}

// EndSessions logs a user out everywhere; it returns how many sessions ended.
func (s *Service) EndSessions(ctx context.Context, username string) (int, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM login_sessions WHERE user_id = (SELECT id FROM users WHERE username = ?)`, username)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// changeUser runs change for the user, then refuses the result when no
// enabled admin is left.
func (s *Service) changeUser(ctx context.Context, username string, change func(tx *sql.Tx, id int64) error) error {
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
	if err := change(tx, id); err != nil {
		return err
	}
	var admins int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0`).Scan(&admins); err != nil {
		return err
	}
	if admins == 0 {
		return ErrLastAdmin
	}
	return tx.Commit()
}

// OIDCAccount is the account of a person signing in with OIDC: found by
// the provider's subject, else made (named after their username, made
// unique). Its role follows their groups at every sign-in. Disabled
// accounts are refused.
func (s *Service) OIDCAccount(ctx context.Context, subject, username, role string) (User, error) {
	if subject == "" {
		return User{}, errors.New("no subject")
	}
	if role != RoleAdmin && role != RoleViewer {
		return User{}, ErrBadRole
	}
	u, err := scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE oidc_subject = ?`, subject))
	switch {
	case err == nil:
		if u.Disabled {
			return User{}, ErrInvalidCredentials
		}
		if u.Role != role {
			if err := s.SetRole(ctx, u.Username, role); err != nil && !errors.Is(err, ErrLastAdmin) {
				return User{}, err
			}
			if err == nil {
				u.Role = role
			}
		}
		return u, nil
	case !errors.Is(err, sql.ErrNoRows):
		return User{}, err
	}
	base := cleanUsername(username)
	now := s.now().Unix()
	for i := 1; i < 100; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		res, err := s.db.ExecContext(ctx, `INSERT INTO users (username, password_hash, created_at, password_changed_at, role, source, oidc_subject)
			VALUES (?, '', ?, ?, ?, 'oidc', ?)`, name, now, now, role, subject)
		if err != nil {
			if strings.Contains(err.Error(), "UNIQUE") && !strings.Contains(err.Error(), "oidc_subject") {
				continue // the name is taken: try the next
			}
			return User{}, err
		}
		id, _ := res.LastInsertId()
		return User{ID: id, Username: name, CreatedAt: time.Unix(now, 0).UTC(), Role: role, Source: SourceOIDC}, nil
	}
	return User{}, fmt.Errorf("%w: no free name for %s", ErrUserExists, base)
}

// cleanUsername makes an OIDC username fit the local rules.
func cleanUsername(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '@', r == '-':
			b.WriteRune(r)
		}
	}
	out := strings.TrimLeft(b.String(), "._@-")
	if len(out) > 60 {
		out = out[:60]
	}
	if out == "" {
		out = "user"
	}
	return out
}

// Users lists all accounts.
func (s *Service) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
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
// passwords, so usernames cannot be probed by timing. Disabled accounts and
// accounts without a password (signed in with OIDC only) are refused.
func (s *Service) Authenticate(ctx context.Context, username, password string) (User, error) {
	var hash string
	row := s.db.QueryRowContext(ctx, `SELECT `+userCols+`, password_hash FROM users WHERE username = ?`, username)
	var u User
	var created, last int64
	err := row.Scan(&u.ID, &u.Username, &created, &u.Role, &u.Source, &u.HasPassword, &u.Disabled, &last, &u.LastLoginIP, &u.LastLoginVia, &hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && hash == "") {
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
	if !ok || u.Disabled {
		return User{}, ErrInvalidCredentials
	}
	u.CreatedAt = time.Unix(created, 0).UTC()
	if last > 0 {
		u.LastLoginAt = time.Unix(last, 0).UTC()
	}
	return u, nil
}

// StartSession creates a login session signed in with a password and
// returns the cookie value.
func (s *Service) StartSession(ctx context.Context, u User, ip, userAgent string) (string, time.Time, error) {
	return s.StartSessionVia(ctx, u, ip, userAgent, "password")
}

// StartSessionVia creates a login session; via is how it signed in
// (password or oidc). The account's last login is noted.
func (s *Service) StartSessionVia(ctx context.Context, u User, ip, userAgent, via string) (token string, expires time.Time, err error) {
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
	_, err = s.db.ExecContext(ctx, `INSERT INTO login_sessions (token_hash, user_id, created_at, last_used, expires_at, ip, user_agent, via)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, hashToken(token), u.ID, now.Unix(), now.Unix(), expires.Unix(), ip, userAgent, via)
	if err != nil {
		return "", time.Time{}, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at = ?, last_login_ip = ?, last_login_via = ? WHERE id = ?`, now.Unix(), ip, via, u.ID)
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
	var disabled bool
	err := s.db.QueryRowContext(ctx, `SELECT u.id, u.username, u.created_at, u.role, u.source, u.disabled, ls.via, ls.last_used, ls.expires_at
		FROM login_sessions ls JOIN users u ON u.id = ls.user_id WHERE ls.token_hash = ?`, h).
		Scan(&sess.User.ID, &sess.User.Username, &created, &sess.User.Role, &sess.User.Source, &disabled, &sess.Via, &lastUsed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNoSession
	}
	if err != nil {
		return Session{}, err
	}
	if disabled || now.Unix() >= expires || now.Unix()-lastUsed >= int64(s.opts.IdleTimeout/time.Second) {
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
