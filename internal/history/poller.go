package history

import (
	"context"
	"log/slog"
	"time"

	"github.com/shuricksumy/trapdoor/internal/store"
)

// Poller records the history of every server the agent reports, with one
// Collector per server id.
type Poller struct {
	DB *store.DB
	// List returns the ids of the servers to record.
	List func(ctx context.Context) ([]string, error)
	// Source returns the live sessions of one server.
	Source   func(id string) Source
	Interval time.Duration // default 30s
	Log      *slog.Logger

	cols map[string]*Collector
}

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	iv := p.Interval
	if iv <= 0 {
		iv = 30 * time.Second
	}
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		p.Poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Poll records one snapshot of every server. Failures are logged; a server
// that cannot be reached keeps its sessions open until the next good poll.
func (p *Poller) Poll(ctx context.Context) {
	ids, err := p.List(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Warn("history poll failed", "err", err)
		}
		return
	}
	if p.cols == nil {
		p.cols = map[string]*Collector{}
	}
	for _, id := range ids {
		c := p.cols[id]
		if c == nil {
			c = &Collector{DB: p.DB, Source: p.Source(id), Agent: id, Log: p.Log}
			p.cols[id] = c
		}
		if err := c.Poll(ctx); err != nil && ctx.Err() == nil {
			p.Log.Warn("history poll failed", "server", id, "err", err)
		}
	}
}
