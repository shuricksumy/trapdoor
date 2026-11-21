package web

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// Speed and usage limits per server (the Limits page).
//
// Each server has defaults; a client can have its own value for any of
// them. A client over its usage limit for the period (day, week from
// Monday, month; in TD_TZ) is slowed down to the "over" speed, or blocked,
// until the period ends or an admin resets it. The web part decides who is
// over (it has the traffic history) and every minute sends the agent the
// speed each client may use now (api.Shaping); the agent applies it with tc.
//
// Everything is kept in the settings table, key "limits": per server, the
// defaults and the clients with their own values or a reset.

const settingLimits = "limits"

// LimitSet holds limits; a nil field follows the server's default.
type LimitSet struct {
	Down    *int64  `json:"down"`     // kbit/s, 0 = unlimited
	Up      *int64  `json:"up"`       // kbit/s, 0 = unlimited
	QuotaMB *int64  `json:"quota_mb"` // usage limit per period in MB (1000 × 1000 bytes), 0 = none
	Period  *string `json:"period"`   // day | week | month
	// Over the usage limit: slowed down to these speeds (kbit/s), or blocked.
	SlowDown *int64 `json:"slow_down"`
	SlowUp   *int64 `json:"slow_up"`
	Block    *bool  `json:"block"`
}

func (l LimitSet) empty() bool {
	return l.Down == nil && l.Up == nil && l.QuotaMB == nil && l.Period == nil && l.SlowDown == nil && l.SlowUp == nil && l.Block == nil
}

// Limit is a LimitSet with every value filled in.
type Limit struct {
	Down     int64  `json:"down"`
	Up       int64  `json:"up"`
	QuotaMB  int64  `json:"quota_mb"`
	Period   string `json:"period"`
	SlowDown int64  `json:"slow_down"`
	SlowUp   int64  `json:"slow_up"`
	Block    bool   `json:"block"`
}

// builtinLimit is the default of a server nobody set limits for.
var builtinLimit = Limit{Period: "month", SlowDown: 1000, SlowUp: 1000}

// over fills in the nil fields of s from base.
func (s LimitSet) over(base Limit) Limit {
	pick := func(p *int64, d int64) int64 {
		if p != nil {
			return *p
		}
		return d
	}
	out := Limit{Down: pick(s.Down, base.Down), Up: pick(s.Up, base.Up), QuotaMB: pick(s.QuotaMB, base.QuotaMB),
		Period: base.Period, SlowDown: pick(s.SlowDown, base.SlowDown), SlowUp: pick(s.SlowUp, base.SlowUp), Block: base.Block}
	if s.Period != nil {
		out.Period = *s.Period
	}
	if s.Block != nil {
		out.Block = *s.Block
	}
	return out
}

const maxKbit = 10_000_000 // 10 Gbit/s

func (s LimitSet) validate() error {
	for _, v := range []struct {
		what string
		p    *int64
	}{{"speed down", s.Down}, {"speed up", s.Up}, {"slowed speed down", s.SlowDown}, {"slowed speed up", s.SlowUp}} {
		if v.p != nil && (*v.p < 0 || *v.p > maxKbit) {
			return fmt.Errorf("%s: 0 (unlimited) to %d kbit/s", v.what, maxKbit)
		}
	}
	if s.QuotaMB != nil && (*s.QuotaMB < 0 || *s.QuotaMB > 1_000_000_000) {
		return errors.New("usage limit: 0 (none) to 1,000,000,000 MB")
	}
	if s.Period != nil && *s.Period != "day" && *s.Period != "week" && *s.Period != "month" {
		return errors.New("period: day, week or month")
	}
	return nil
}

type limitClient struct {
	Own LimitSet `json:"own"`
	// ResetAt: an admin reset the client's usage; what it had used in the
	// period then (ResetUsed) no longer counts.
	ResetAt   int64 `json:"reset_at,omitempty"`
	ResetUsed int64 `json:"reset_used,omitempty"`
	// Notified: the last message sent, "<period start>:<80|100>".
	Notified string `json:"notified,omitempty"`
}

type serverLimits struct {
	Default LimitSet                `json:"default"`
	Clients map[string]*limitClient `json:"clients"`
}

// LimitRow is one client on the Limits page.
type LimitRow struct {
	Name        string     `json:"name"`
	Own         LimitSet   `json:"own"`
	Limit       Limit      `json:"limit"` // with the defaults filled in
	Used        int64      `json:"used"`  // bytes in the period (since a reset)
	PeriodStart time.Time  `json:"period_start"`
	PeriodEnd   time.Time  `json:"period_end"`
	ResetAt     *time.Time `json:"reset_at,omitempty"`
	// State: full (no speed limit), capped (its normal speed limit),
	// slowed or blocked (over its usage limit).
	State string    `json:"state"`
	Speed api.Speed `json:"speed"` // what it gets now
}

// Limits is GET /api/v1/servers/{server}/limits.
type Limits struct {
	Default Limit      `json:"default"`
	Own     LimitSet   `json:"own_default"` // what was set for the server (nil = built in)
	Clients []LimitRow `json:"clients"`
	// Shaping is the agent's state of the speed rules (nil when it could
	// not be asked; ShapingError says why).
	Shaping      *api.ShapingState `json:"shaping,omitempty"`
	ShapingError string            `json:"shaping_error,omitempty"`
}

func (s *Server) registerLimitRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/servers/{server}/limits", s.handleGetLimits)
	mux.HandleFunc("PUT /api/v1/servers/{server}/limits/default", s.handleSetLimitDefault)
	mux.HandleFunc("PUT /api/v1/servers/{server}/limits/clients/{name}", s.handleSetLimitClient)
	mux.HandleFunc("POST /api/v1/servers/{server}/limits/clients/{name}/reset", s.handleResetLimitClient)
}

func (s *Server) loadLimits(ctx context.Context) (map[string]*serverLimits, error) {
	all := map[string]*serverLimits{}
	if _, err := s.Stats.DB.Setting(ctx, settingLimits, &all); err != nil {
		return nil, err
	}
	for _, sl := range all {
		if sl.Clients == nil {
			sl.Clients = map[string]*limitClient{}
		}
	}
	return all, nil
}

// editLimits changes a server's limits under the lock and saves them.
func (s *Server) editLimits(ctx context.Context, server string, edit func(sl *serverLimits, now time.Time) error) error {
	s.limitsMu.Lock()
	defer s.limitsMu.Unlock()
	all, err := s.loadLimits(ctx)
	if err != nil {
		return err
	}
	sl := all[server]
	if sl == nil {
		sl = &serverLimits{Clients: map[string]*limitClient{}}
		all[server] = sl
	}
	if err := edit(sl, s.Now()); err != nil {
		return err
	}
	return s.Stats.DB.SetSetting(ctx, settingLimits, all)
}

// periodOf is the period containing now: [start, end) in loc.
func periodOf(period string, now time.Time, loc *time.Location) (time.Time, time.Time) {
	t := now.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	switch period {
	case "day":
		return day, day.AddDate(0, 0, 1)
	case "week":
		start := day.AddDate(0, 0, -((int(t.Weekday()) + 6) % 7)) // from Monday
		return start, start.AddDate(0, 0, 7)
	default:
		start := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
		return start, start.AddDate(0, 1, 0)
	}
}

// evaluate works out each client's row. names: the clients to show (the
// clients of the server, those with settings and those with usage).
func (s *Server) evaluate(ctx context.Context, server string, sl *serverLimits, names []string) ([]LimitRow, error) {
	if sl == nil {
		sl = &serverLimits{Clients: map[string]*limitClient{}}
	}
	def := sl.Default.over(builtinLimit)
	now := s.Now()
	usage := map[int64]map[string]int64{} // by period start
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	for n := range sl.Clients {
		set[n] = true
	}
	rows := make([]LimitRow, 0, len(set))
	// clients with usage in the longest period count too
	for _, p := range []string{"month", "week"} {
		start, _ := periodOf(p, now, s.Location)
		u, err := s.Stats.DB.Usage(ctx, server, start.Unix())
		if err != nil {
			return nil, err
		}
		usage[start.Unix()] = u
		for n := range u {
			set[n] = true
		}
	}
	for n := range set {
		c := sl.Clients[n]
		if c == nil {
			c = &limitClient{}
		}
		lim := c.Own.over(def)
		start, end := periodOf(lim.Period, now, s.Location)
		u, ok := usage[start.Unix()]
		if !ok {
			var err error
			if u, err = s.Stats.DB.Usage(ctx, server, start.Unix()); err != nil {
				return nil, err
			}
			usage[start.Unix()] = u
		}
		row := LimitRow{Name: n, Own: c.Own, Limit: lim, Used: u[n], PeriodStart: start, PeriodEnd: end}
		if c.ResetAt >= start.Unix() && c.ResetAt > 0 {
			row.Used = max(0, row.Used-c.ResetUsed)
			at := time.Unix(c.ResetAt, 0).UTC()
			row.ResetAt = &at
		}
		row.State, row.Speed = "full", api.Speed{Down: lim.Down, Up: lim.Up}
		if lim.Down > 0 || lim.Up > 0 {
			row.State = "capped"
		}
		if lim.QuotaMB > 0 && row.Used >= lim.QuotaMB*1_000_000 {
			if lim.Block {
				row.State, row.Speed = "blocked", api.Speed{Block: true}
			} else {
				row.State, row.Speed = "slowed", api.Speed{Down: slower(lim.SlowDown, lim.Down), Up: slower(lim.SlowUp, lim.Up)}
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	return rows, nil
}

// slower: the slowed speed never exceeds the normal one (0 = unlimited).
func slower(slow, normal int64) int64 {
	if slow == 0 || (normal > 0 && normal < slow) {
		return normal
	}
	return slow
}

// shapingFor is what the agent gets: the default speed, and every client
// whose speed differs from it.
func shapingFor(def Limit, rows []LimitRow) api.Shaping {
	out := api.Shaping{Default: api.Speed{Down: def.Down, Up: def.Up}, Clients: map[string]api.Speed{}}
	for _, r := range rows {
		if r.Speed != out.Default {
			out.Clients[r.Name] = r.Speed
		}
	}
	return out
}

// clientNames are the server's clients (OpenVPN: valid certificates;
// WireGuard: peers).
func (s *Server) clientNames(ctx context.Context, c AgentClient) []string {
	var out []string
	if peers, err := c.Peers(ctx); err == nil {
		for _, p := range peers {
			out = append(out, p.Name)
		}
		return out
	}
	if certs, err := c.ListCerts(ctx); err == nil {
		for _, cert := range certs {
			if cert.Current && cert.Status != "revoked" {
				out = append(out, cert.Name)
			}
		}
	}
	return out
}

// applyLimits works out the speeds of a server's clients now and sends
// them to its agent. notify: send the 80% / 100% messages (the minute loop).
func (s *Server) applyLimits(ctx context.Context, server string, names []string, notify bool) (Limits, error) {
	s.limitsMu.Lock()
	all, err := s.loadLimits(ctx)
	if err != nil {
		s.limitsMu.Unlock()
		return Limits{}, err
	}
	sl := all[server]
	rows, err := s.evaluate(ctx, server, sl, names)
	if err != nil {
		s.limitsMu.Unlock()
		return Limits{}, err
	}
	var events []LimitRow
	if notify && sl != nil {
		changed := false
		for _, r := range rows {
			if r.Limit.QuotaMB <= 0 {
				continue
			}
			level := 0
			switch used := r.Used; {
			case used >= r.Limit.QuotaMB*1_000_000:
				level = 100
			case used*10 >= r.Limit.QuotaMB*1_000_000*8:
				level = 80
			}
			if level == 0 {
				continue
			}
			c := sl.Clients[r.Name]
			if c == nil {
				c = &limitClient{}
			}
			mark := fmt.Sprintf("%d:%d", r.PeriodStart.Unix(), level)
			prev := c.Notified
			if prev == mark || (level == 80 && prev == fmt.Sprintf("%d:100", r.PeriodStart.Unix())) {
				continue
			}
			c.Notified = mark
			sl.Clients[r.Name] = c
			changed = true
			events = append(events, r)
		}
		if changed {
			if err := s.Stats.DB.SetSetting(ctx, settingLimits, all); err != nil {
				s.Log.Warn("limits: save", "err", err)
			}
		}
	}
	s.limitsMu.Unlock()

	out := Limits{Clients: rows}
	if sl != nil {
		out.Own = sl.Default
		out.Default = sl.Default.over(builtinLimit)
	} else {
		out.Default = builtinLimit
	}
	st, err := s.Agent.Server(server).SetShaping(ctx, shapingFor(out.Default, rows))
	if err != nil {
		out.ShapingError = shapingError(err)
	} else {
		out.Shaping = &st
	}
	if len(events) > 0 {
		s.reportLimits(ctx, server, events)
	}
	return out, nil
}

func shapingError(err error) string {
	var ae *agent.APIError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

// WatchLimits applies the limits of every server that has any, every
// minute: clients go over their usage limit, periods end, OpenVPN clients
// get new addresses when they reconnect.
func (s *Server) WatchLimits(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.checkLimits(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) checkLimits(ctx context.Context) {
	all, err := s.loadLimits(ctx)
	if err != nil {
		s.Log.Warn("limits: load", "err", err)
		return
	}
	for server := range all {
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		if out, err := s.applyLimits(cctx, server, nil, true); err != nil {
			s.Log.Warn("limits: apply", "server", server, "err", err)
		} else if out.ShapingError != "" {
			s.Log.Warn("limits: speeds not sent", "server", server, "err", out.ShapingError)
		}
		cancel()
	}
}

func (s *Server) handleGetLimits(w http.ResponseWriter, r *http.Request) {
	server := r.PathValue("server")
	c := s.agent(r)
	names := s.clientNames(r.Context(), c)
	s.limitsMu.Lock()
	all, err := s.loadLimits(r.Context())
	var rows []LimitRow
	if err == nil {
		rows, err = s.evaluate(r.Context(), server, all[server], names)
	}
	s.limitsMu.Unlock()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := Limits{Default: builtinLimit, Clients: rows}
	if sl := all[server]; sl != nil {
		out.Own, out.Default = sl.Default, sl.Default.over(builtinLimit)
	}
	if st, err := c.Shaping(r.Context()); err != nil {
		out.ShapingError = shapingError(err)
	} else {
		out.Shaping = &st
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetLimitDefault(w http.ResponseWriter, r *http.Request) {
	var in LimitSet
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := in.validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	server := r.PathValue("server")
	if err := s.editLimits(r.Context(), server, func(sl *serverLimits, _ time.Time) error {
		sl.Default = in
		return nil
	}); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "limits.default", server)
	s.writeApplied(w, r)
}

func validLimitName(name string) bool {
	return name != "" && len(name) <= 128 && !strings.ContainsAny(name, "/\\\n\r\t")
}

func (s *Server) handleSetLimitClient(w http.ResponseWriter, r *http.Request) {
	var in LimitSet
	if !decodeJSON(w, r, &in) {
		return
	}
	name := r.PathValue("name")
	if !validLimitName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "bad client name")
		return
	}
	if err := in.validate(); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.editLimits(r.Context(), r.PathValue("server"), func(sl *serverLimits, _ time.Time) error {
		c := sl.Clients[name]
		if c == nil {
			c = &limitClient{}
		}
		c.Own = in
		if c.Own.empty() && c.ResetAt == 0 && c.Notified == "" {
			delete(sl.Clients, name)
		} else {
			sl.Clients[name] = c
		}
		return nil
	}); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "limits.client", name)
	s.writeApplied(w, r)
}

// handleResetLimitClient starts the client's usage from zero for the rest
// of the period (and gives it its speed back).
func (s *Server) handleResetLimitClient(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !validLimitName(name) {
		httpx.WriteError(w, http.StatusBadRequest, "bad client name")
		return
	}
	server := r.PathValue("server")
	if err := s.editLimits(r.Context(), server, func(sl *serverLimits, now time.Time) error {
		c := sl.Clients[name]
		if c == nil {
			c = &limitClient{}
		}
		start, _ := periodOf(c.Own.over(sl.Default.over(builtinLimit)).Period, now, s.Location)
		u, err := s.Stats.DB.Usage(r.Context(), server, start.Unix())
		if err != nil {
			return err
		}
		c.ResetAt, c.ResetUsed, c.Notified = now.Unix(), u[name], ""
		sl.Clients[name] = c
		return nil
	}); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "limits.reset", name)
	s.writeApplied(w, r)
}

// writeApplied applies the server's limits now and answers with them.
func (s *Server) writeApplied(w http.ResponseWriter, r *http.Request) {
	out, err := s.applyLimits(r.Context(), r.PathValue("server"), s.clientNames(r.Context(), s.agent(r)), false)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// reportLimits tells the admins about clients at 80% or 100% of their limit.
func (s *Server) reportLimits(ctx context.Context, server string, rows []LimitRow) {
	names := s.serverNames(ctx, server)
	srv := names[server]
	if srv == "" {
		srv = server
	}
	loc := s.Location
	for _, r := range rows {
		who := r.Name
		if n := names[server+"/"+r.Name]; n != "" {
			who = n + " (" + r.Name + ")"
		}
		limit := fmt.Sprintf("%s per %s", formatGB(r.Limit.QuotaMB), r.Limit.Period)
		until := r.PeriodEnd.In(loc).Format("Jan 2 15:04")
		var subject, text string
		switch r.State {
		case "blocked":
			subject = who + " reached its usage limit"
			text = fmt.Sprintf("⛔ %s used %s of its %s on %s and is blocked until %s.", who, formatBytes(r.Used), limit, srv, until)
		case "slowed":
			subject = who + " reached its usage limit"
			text = fmt.Sprintf("🐢 %s used %s of its %s on %s and is slowed down to %s until %s.", who, formatBytes(r.Used), limit, srv, formatSpeed(r.Speed), until)
		default:
			subject = who + " used 80% of its usage limit"
			text = fmt.Sprintf("⚠️ %s used %s of its %s on %s (%d%%). The period ends %s.", who, formatBytes(r.Used), limit, srv,
				int(math.Floor(float64(r.Used)*100/float64(r.Limit.QuotaMB*1_000_000))), until)
		}
		s.notify(ctx, func(n Notify) bool { return n.UsageLimit }, subject, text)
	}
}

// serverNames: the server's name and its OpenVPN clients' display names
// ("server" → name, "server/client" → name).
func (s *Server) serverNames(ctx context.Context, server string) map[string]string {
	out := map[string]string{}
	if s.Agent == nil {
		return out
	}
	if info, err := s.Agent.Info(ctx); err == nil {
		for _, srv := range info.Servers {
			if srv.ID == server {
				out[server] = srv.Name
			}
		}
	}
	if certs, err := s.Agent.Server(server).ListCerts(ctx); err == nil {
		for _, c := range certs {
			if c.Notes != "" && c.Current {
				out[server+"/"+c.Name] = c.Notes
			}
		}
	}
	return out
}

func formatGB(mb int64) string {
	if mb < 1000 {
		return fmt.Sprintf("%d MB", mb)
	}
	if mb%1000 == 0 {
		return fmt.Sprintf("%d GB", mb/1000)
	}
	return fmt.Sprintf("%.1f GB", float64(mb)/1000)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(b)/1e9)
	case b >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	}
	return fmt.Sprintf("%d kB", b/1000)
}

func formatSpeed(sp api.Speed) string {
	one := func(k int64) string {
		switch {
		case k == 0:
			return "unlimited"
		case k%1000 == 0:
			return fmt.Sprintf("%d Mbit/s", k/1000)
		case k > 1000:
			return fmt.Sprintf("%.1f Mbit/s", float64(k)/1000)
		}
		return fmt.Sprintf("%d kbit/s", k)
	}
	if sp.Down == sp.Up {
		return one(sp.Down)
	}
	return one(sp.Down) + " down / " + one(sp.Up) + " up"
}
