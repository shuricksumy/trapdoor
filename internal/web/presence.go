package web

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/mail"
	"github.com/shuricksumy/trapdoor/internal/telegram"
)

const settingPresence = "presence"

// PresenceSettings is GET / PUT /api/v1/presence: where clients' online /
// offline changes go, one email address and / or one Telegram chat. They are
// sent through the Email and Telegram settings; which clients is chosen per
// client.
type PresenceSettings struct {
	Email string `json:"email"` // "" = no email
	Chat  string `json:"chat"`  // "" = no Telegram message
	// GraceMinutes: a client is reported offline only after this long, so a
	// quick reconnect stays quiet (and so is its "online" after it).
	GraceMinutes int `json:"grace_minutes"`
}

func (s *Server) presenceSettings(ctx context.Context) (PresenceSettings, error) {
	ps := PresenceSettings{GraceMinutes: 2}
	_, err := s.Stats.DB.Setting(ctx, settingPresence, &ps)
	if ps.GraceMinutes <= 0 {
		ps.GraceMinutes = 2
	}
	return ps, err
}

func (s *Server) registerPresenceRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/presence", s.handleGetPresence)
	mux.HandleFunc("PUT /api/v1/presence", s.handleSetPresence)
	mux.HandleFunc("GET /api/v1/servers/{server}/notify", s.handleClientNotify)
	mux.HandleFunc("PUT /api/v1/servers/{server}/notify/{name}", s.handleSetClientNotify)
}

func (s *Server) handleGetPresence(w http.ResponseWriter, r *http.Request) {
	ps, err := s.presenceSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ps)
}

func (s *Server) handleSetPresence(w http.ResponseWriter, r *http.Request) {
	var req PresenceSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Email = strings.TrimSpace(req.Email); req.Email != "" {
		a, err := mail.ParseAddresses([]string{req.Email})
		if err != nil || len(a) != 1 {
			httpx.WriteError(w, http.StatusBadRequest, "email: one address, like you@example.com")
			return
		}
		req.Email = a[0]
	}
	if req.Chat = strings.TrimSpace(req.Chat); req.Chat != "" {
		c, err := telegram.ParseChats([]string{req.Chat})
		if err != nil || len(c) != 1 {
			httpx.WriteError(w, http.StatusBadRequest, "Telegram chat: one chat id, like 123456789 or @channel")
			return
		}
		req.Chat = c[0]
	}
	if req.GraceMinutes < 1 || req.GraceMinutes > 120 {
		httpx.WriteError(w, http.StatusBadRequest, "report offline after 1 to 120 minutes")
		return
	}
	if err := s.Stats.DB.SetSetting(r.Context(), settingPresence, req); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "presence.settings", fmt.Sprint(req.GraceMinutes))
	httpx.WriteJSON(w, http.StatusOK, req)
}

func (s *Server) handleClientNotify(w http.ResponseWriter, r *http.Request) {
	m, err := s.Stats.DB.ClientNotify(r.Context(), r.PathValue("server"))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := m[r.PathValue("server")]
	if out == nil {
		out = map[string]bool{}
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetClientNotify(w http.ResponseWriter, r *http.Request) {
	var f struct {
		On bool `json:"on"`
	}
	if !decodeJSON(w, r, &f) {
		return
	}
	server, name := r.PathValue("server"), r.PathValue("name")
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "/\\\n") {
		httpx.WriteError(w, http.StatusBadRequest, "bad client name")
		return
	}
	if err := s.Stats.DB.SetClientNotify(r.Context(), server, name, f.On); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "client.notify", server+"/"+name)
	httpx.WriteJSON(w, http.StatusOK, f)
}

// presenceState is what the watcher knows about one client.
type presenceState struct {
	online   bool
	since    time.Time // the last change
	start    time.Time // when it came online (kept while offline, for how long it lasted)
	from     string    // real address while online
	reported bool      // offline: already reported
}

type presenceWatch struct {
	mu      sync.Mutex
	started time.Time
	clients map[string]*presenceState // "agent/name"
}

// WatchPresence reports clients going online and offline, for the clients
// that have it turned on, every 15 seconds.
func (s *Server) WatchPresence(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		s.checkPresence(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// presenceEvent is one change to report.
type presenceEvent struct {
	agent, name string
	online      bool
	at          time.Time
	from        string
	lasted      time.Duration // how long the client was online (offline events)
}

// checkPresence compares who is connected with the last check. The first
// check only learns the state. An "online" is reported when the client comes
// back after its "offline" was reported (or after being offline at least the
// grace time); an "offline" once the client stayed away the grace time.
func (s *Server) checkPresence(ctx context.Context) {
	online, err := s.Stats.DB.Online(ctx)
	if err != nil {
		s.Log.Warn("presence check failed", "err", err)
		return
	}
	ps, _ := s.presenceSettings(ctx)
	grace := time.Duration(ps.GraceMinutes) * time.Minute
	now := s.Now().UTC()

	w := &s.presence
	w.mu.Lock()
	first := w.clients == nil
	if first {
		w.clients = map[string]*presenceState{}
		w.started = now
	}
	var events []presenceEvent
	seen := map[string]bool{}
	for _, p := range online {
		k := p.Agent + "/" + p.Name
		seen[k] = true
		st := w.clients[k]
		switch {
		case first:
			w.clients[k] = &presenceState{online: true, since: p.Since, start: p.Since, from: p.From}
		case st == nil: // offline since the watcher started
			if now.Sub(w.started) >= grace {
				events = append(events, presenceEvent{agent: p.Agent, name: p.Name, online: true, at: p.Since, from: p.From})
			}
			w.clients[k] = &presenceState{online: true, since: p.Since, start: p.Since, from: p.From}
		case !st.online:
			if st.reported || now.Sub(st.since) >= grace {
				events = append(events, presenceEvent{agent: p.Agent, name: p.Name, online: true, at: p.Since, from: p.From})
			}
			*st = presenceState{online: true, since: p.Since, start: p.Since, from: p.From}
		default:
			st.from = p.From
		}
	}
	for k, st := range w.clients {
		if st.online && !seen[k] {
			// gone: offline since its session ended (or since now, if unknown)
			st.online, st.reported, st.since = false, false, now
			agent, name, _ := strings.Cut(k, "/")
			if end, err := s.Stats.DB.LastDisconnect(ctx, agent, name); err == nil && !end.IsZero() && end.After(st.start) && !end.After(now) {
				st.since = end
			}
		}
		if !st.online && !st.reported && now.Sub(st.since) >= grace {
			st.reported = true
			agent, name, _ := strings.Cut(k, "/")
			ev := presenceEvent{agent: agent, name: name, at: st.since}
			if !st.start.IsZero() {
				ev.lasted = st.since.Sub(st.start)
			}
			events = append(events, ev)
		}
	}
	w.mu.Unlock()
	if len(events) > 0 {
		s.reportPresence(ctx, ps, events)
	}
}

// reportPresence sends the events of the clients that have it turned on.
func (s *Server) reportPresence(ctx context.Context, ps PresenceSettings, events []presenceEvent) {
	flags, err := s.Stats.DB.ClientNotify(ctx, "")
	if err != nil {
		return
	}
	es, _ := s.emailSettings(ctx)
	ts, _ := s.telegramSettings(ctx)
	byEmail := ps.Email != "" && es.on()
	byTelegram := ps.Chat != "" && ts.on()
	if !byEmail && !byTelegram {
		return
	}
	names := s.presenceNames(ctx, events, flags)
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	for _, ev := range events {
		if !flags[ev.agent][ev.name] {
			continue
		}
		subject, text := s.presenceMessage(ev, names)
		if byEmail {
			s.mailWG.Add(1)
			go func() {
				defer s.mailWG.Done()
				_ = s.sendMail(context.WithoutCancel(ctx), es.Config, mail.Message{To: []string{ps.Email}, Subject: "trapdoor: " + subject,
					Text: text + "\n\n-- \ntrapdoor" + s.baseURLLine()})
			}()
		}
		if byTelegram {
			s.mailWG.Add(1)
			go func() {
				defer s.mailWG.Done()
				_ = s.sendTelegram(context.WithoutCancel(ctx), ts.Token, []string{ps.Chat}, text)
			}()
		}
	}
}

// presenceNames are the servers' names and the OpenVPN clients' display
// names for the events to send ("agent" → name, "agent/client" → name).
func (s *Server) presenceNames(ctx context.Context, events []presenceEvent, flags map[string]map[string]bool) map[string]string {
	out := map[string]string{}
	if s.Agent == nil {
		return out
	}
	want := map[string]bool{}
	for _, ev := range events {
		if flags[ev.agent][ev.name] {
			want[ev.agent] = true
		}
	}
	if len(want) == 0 {
		return out
	}
	info, err := s.Agent.Info(ctx)
	if err != nil {
		return out
	}
	for _, srv := range info.Servers {
		if !want[srv.ID] {
			continue
		}
		out[srv.ID] = srv.Name
		if srv.Type != api.TypeOpenVPN || !srv.PKIReady {
			continue
		}
		if certs, err := s.Agent.Server(srv.ID).ListCerts(ctx); err == nil {
			for _, c := range certs {
				if c.Notes != "" && c.Current {
					out[srv.ID+"/"+c.Name] = c.Notes
				}
			}
		}
	}
	return out
}

func (s *Server) presenceMessage(ev presenceEvent, names map[string]string) (subject, text string) {
	who := ev.name
	if n := names[ev.agent+"/"+ev.name]; n != "" {
		who = n + " (" + ev.name + ")"
	}
	server := names[ev.agent]
	if server == "" {
		server = ev.agent
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	at := ev.at.In(loc).Format("Jan 2 15:04")
	if ev.online {
		subject = fmt.Sprintf("%s is online", who)
		text = fmt.Sprintf("🟢 %s is online on %s since %s", who, server, at)
		if ev.from != "" {
			text += " from " + ev.from
		}
		return subject, text + "."
	}
	subject = fmt.Sprintf("%s is offline", who)
	text = fmt.Sprintf("🔴 %s is offline on %s since %s", who, server, at)
	if ev.lasted > 0 {
		text += fmt.Sprintf(" (was online %s)", shortDuration(ev.lasted))
	}
	return subject, text + "."
}

func shortDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h %d min", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d d %d h", int(d.Hours())/24, int(d.Hours())%24)
	}
}
