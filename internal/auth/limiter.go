package auth

import (
	"strings"
	"sync"
	"time"
)

// Limiter throttles failed logins per client IP and per username: after too
// many failures within Window, further attempts are refused until the window
// has passed since the last failure. The per-username limit is looser so an
// attacker cannot easily lock the real admin out.
type Limiter struct {
	MaxFailures     int           // per IP, default 10
	MaxUserFailures int           // per username, default 30
	Window          time.Duration // default 15 minutes
	Now             func() time.Time

	mu    sync.Mutex
	fails map[string]*failures
}

type failures struct {
	n    int
	last time.Time
}

func (l *Limiter) init() {
	if l.fails == nil {
		l.fails = map[string]*failures{}
	}
	if l.MaxFailures == 0 {
		l.MaxFailures = 10
	}
	if l.MaxUserFailures == 0 {
		l.MaxUserFailures = 30
	}
	if l.Window == 0 {
		l.Window = 15 * time.Minute
	}
	if l.Now == nil {
		l.Now = time.Now
	}
}

func keys(ip, username string) []string {
	return []string{"ip:" + ip, "user:" + strings.ToLower(username)}
}

// Allowed reports whether a login attempt may proceed, and if not, how long
// to wait.
func (l *Limiter) Allowed(ip, username string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.init()
	now := l.Now()
	for _, k := range keys(ip, username) {
		f, ok := l.fails[k]
		if !ok {
			continue
		}
		if now.Sub(f.last) >= l.Window {
			delete(l.fails, k)
			continue
		}
		limit := l.MaxFailures
		if strings.HasPrefix(k, "user:") {
			limit = l.MaxUserFailures
		}
		if f.n >= limit {
			return false, l.Window - now.Sub(f.last)
		}
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.init()
	now := l.Now()
	for _, k := range keys(ip, username) {
		f, ok := l.fails[k]
		if !ok || now.Sub(f.last) >= l.Window {
			f = &failures{}
			l.fails[k] = f
		}
		f.n++
		f.last = now
	}
	if len(l.fails) > 10000 { // bound memory under a spray attack
		for k, f := range l.fails {
			if now.Sub(f.last) >= l.Window {
				delete(l.fails, k)
			}
		}
	}
}

// Succeed clears failures for the username (not the IP).
func (l *Limiter) Succeed(username string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.init()
	delete(l.fails, "user:"+strings.ToLower(username))
}
