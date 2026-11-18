package web

import (
	"context"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
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
	info, err := s.Agent.Info(ctx)
	if err != nil {
		return // agent unreachable; the next check will try again
	}
	for _, srv := range info.Servers {
		s.checkServerExpiry(ctx, srv)
	}
}

func (s *Server) checkServerExpiry(ctx context.Context, srv api.ServerInfo) {
	now := s.Now()
	log := s.Log.With("server", srv.ID)
	if !srv.CANotAfter.IsZero() {
		if days := int(srv.CANotAfter.Sub(now).Hours() / 24); days < CAWarnDays {
			log.Warn("CA certificate expires soon: every client stops working then", "ca", srv.CAName, "days", days, "not_after", srv.CANotAfter)
		}
	}
	if !srv.PKIReady {
		return
	}
	certs, err := s.Agent.Server(srv.ID).ListCerts(ctx)
	if err != nil {
		return
	}
	limit := now.Add(time.Duration(s.ExpiryWarnDays) * 24 * time.Hour)
	n := 0
	for _, c := range certs {
		if !c.Current || c.Status == "revoked" || c.NotAfter.After(limit) {
			continue
		}
		n++
		log.Warn("certificate expires soon", "name", c.Name, "kind", c.Kind, "status", c.Status,
			"days", int(c.NotAfter.Sub(now).Hours()/24), "not_after", c.NotAfter)
	}
	if n > 0 {
		log.Warn("certificates need renewing", "count", n, "within_days", s.ExpiryWarnDays)
	}
}
