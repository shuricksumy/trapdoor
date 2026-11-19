package web

import (
	"context"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
)

// CAWarnDays is when the CA's own expiry is flagged: when it expires, every
// certificate it signed stops working at once.
const CAWarnDays = 90

// PeerWarnDays is how early a WireGuard / AmneziaWG client's end date is
// emailed.
const PeerWarnDays = 3

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
	var expiring []expiringCert
	for _, srv := range info.Servers {
		expiring = append(expiring, s.checkServerExpiry(ctx, srv)...)
	}
	s.notifyExpiry(ctx, expiring)
}

func (s *Server) checkServerExpiry(ctx context.Context, srv api.ServerInfo) []expiringCert {
	now := s.Now()
	log := s.Log.With("server", srv.ID)
	if !srv.CANotAfter.IsZero() {
		if days := int(srv.CANotAfter.Sub(now).Hours() / 24); days < CAWarnDays {
			log.Warn("CA certificate expires soon: every client stops working then", "ca", srv.CAName, "days", days, "not_after", srv.CANotAfter)
		}
	}
	var out []expiringCert
	name := srv.Name
	if name == "" {
		name = srv.ID
	}
	if !srv.CANotAfter.IsZero() && srv.CANotAfter.Before(now.Add(CAWarnDays*24*time.Hour)) {
		out = append(out, expiringCert{server: name, name: "the CA (every client)", notAfter: srv.CANotAfter})
	}
	if srv.Type == api.TypeWireGuard || srv.Type == api.TypeAmneziaWG {
		return append(out, s.peerExpiry(ctx, srv.ID, name)...)
	}
	if !srv.PKIReady {
		return out
	}
	certs, err := s.Agent.Server(srv.ID).ListCerts(ctx)
	if err != nil {
		return out
	}
	limit := now.Add(time.Duration(s.ExpiryWarnDays) * 24 * time.Hour)
	n := 0
	for _, c := range certs {
		if !c.Current || c.Status == "revoked" || c.NotAfter.After(limit) {
			continue
		}
		n++
		out = append(out, expiringCert{server: name, name: c.Name, notAfter: c.NotAfter})
		log.Warn("certificate expires soon", "name", c.Name, "kind", c.Kind, "status", c.Status,
			"days", int(c.NotAfter.Sub(now).Hours()/24), "not_after", c.NotAfter)
	}
	if n > 0 {
		log.Warn("certificates need renewing", "count", n, "within_days", s.ExpiryWarnDays)
	}
	return out
}

// peerExpiry lists a WireGuard server's clients whose end date is near.
func (s *Server) peerExpiry(ctx context.Context, id, server string) []expiringCert {
	peers, err := s.Agent.Server(id).Peers(ctx)
	if err != nil {
		return nil
	}
	limit := s.Now().Add(PeerWarnDays * 24 * time.Hour)
	var out []expiringCert
	for _, p := range peers {
		if p.ExpiresAt != nil && !p.Disabled && !p.Expired && p.ExpiresAt.Before(limit) {
			out = append(out, expiringCert{server: server, name: p.Name, notAfter: *p.ExpiresAt})
		}
	}
	return out
}
