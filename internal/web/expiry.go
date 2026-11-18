package web

import (
	"context"
	"time"
)

// CAWarnDays is when the CA's own expiry is flagged: when it expires, every
// certificate it signed stops working at once.
const CAWarnDays = 90

// WatchExpiry logs expiring certificates at start and then once a day.
func (s *Server) WatchExpiry(ctx context.Context) {
	t := time.NewTicker(24 * time.Hour)
	defer t.Stop()
	for {
		s.checkExpiry(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) checkExpiry(ctx context.Context) {
	now := s.Now()
	if info, err := s.Agent.Info(ctx); err == nil && !info.CANotAfter.IsZero() {
		if days := int(info.CANotAfter.Sub(now).Hours() / 24); days < CAWarnDays {
			s.Log.Warn("CA certificate expires soon: every client stops working then", "ca", info.CAName, "days", days, "not_after", info.CANotAfter)
		}
	}
	certs, err := s.Agent.ListCerts(ctx)
	if err != nil {
		return // agent unreachable; the next check will try again
	}
	limit := now.Add(time.Duration(s.ExpiryWarnDays) * 24 * time.Hour)
	n := 0
	for _, c := range certs {
		if !c.Current || c.Status == "revoked" || c.NotAfter.After(limit) {
			continue
		}
		n++
		s.Log.Warn("certificate expires soon", "name", c.Name, "kind", c.Kind, "status", c.Status,
			"days", int(c.NotAfter.Sub(now).Hours()/24), "not_after", c.NotAfter)
	}
	if n > 0 {
		s.Log.Warn("certificates need renewing", "count", n, "within_days", s.ExpiryWarnDays)
	}
}
