package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
)

// parseQuery reads ?preset= or ?from=&to= (YYYY-MM-DD, inclusive, in the
// configured time zone), ?group=day|month|year and ?name=. Default: this month.
func (s *Server) parseQuery(r *http.Request) (history.Query, error) {
	v := r.URL.Query()
	q := history.Query{Group: history.Group(v.Get("group")), Name: v.Get("name"), Loc: s.Location}
	if q.Name != "" && !pki.ValidName(q.Name) {
		return q, fmt.Errorf("%w: invalid name", history.ErrBadQuery)
	}
	var err error
	switch {
	case v.Get("preset") != "":
		q.From, q.To, err = history.Preset(v.Get("preset"), s.Now(), s.Location)
	case v.Get("from") != "" || v.Get("to") != "":
		if q.From, err = time.ParseInLocation(time.DateOnly, v.Get("from"), s.Location); err != nil {
			return q, fmt.Errorf("%w: from must be YYYY-MM-DD", history.ErrBadQuery)
		}
		if q.To, err = time.ParseInLocation(time.DateOnly, v.Get("to"), s.Location); err != nil {
			return q, fmt.Errorf("%w: to must be YYYY-MM-DD", history.ErrBadQuery)
		}
	default:
		q.From, q.To, err = history.Preset("this_month", s.Now(), s.Location)
	}
	return q, err
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	rep, err := s.stats(r).Report(r.Context(), q)
	if err != nil {
		s.queryError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

func (s *Server) handleStatsCSV(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	rep, err := s.stats(r).Report(r.Context(), q)
	if err != nil {
		s.queryError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="trapdoor-%s_%s.csv"`, rep.From, rep.To))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"period", "name", "upload_bytes", "download_bytes", "estimated"})
	for _, row := range rep.Rows {
		_ = cw.Write([]string{row.Period, row.Name,
			strconv.FormatInt(row.Upload, 10), strconv.FormatInt(row.Download, 10),
			strconv.FormatBool(row.Estimated)})
	}
	cw.Flush()
}

func (s *Server) handleHistorySessions(w http.ResponseWriter, r *http.Request) {
	q, err := s.parseQuery(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	from := time.Date(q.From.Year(), q.From.Month(), q.From.Day(), 0, 0, 0, 0, s.Location)
	to := time.Date(q.To.Year(), q.To.Month(), q.To.Day(), 0, 0, 0, 0, s.Location).AddDate(0, 0, 1)
	if !to.After(from) {
		s.queryError(w, fmt.Errorf("%w: to is before from", history.ErrBadQuery))
		return
	}
	list, err := s.stats(r).Sessions(r.Context(), q.Name, from, to, limit)
	if err != nil {
		s.queryError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (s *Server) queryError(w http.ResponseWriter, err error) {
	if errors.Is(err, history.ErrBadQuery) {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Error("stats", "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}

// handleUptime is GET /api/v1/servers/{server}/history/uptime: every
// client's time online by hour (range=24h, 7d) or by day (range=30d, or
// month=YYYY-MM for a calendar), in the web's time zone.
func (s *Server) handleUptime(w http.ResponseWriter, r *http.Request) {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	now := s.Now().In(loc)
	q := r.URL.Query()
	var from, to time.Time
	step := history.StepHour
	if q.Get("from") != "" || q.Get("to") != "" {
		f, t, err := s.dateRange(r)
		if err != nil {
			s.queryError(w, err)
			return
		}
		from, to, step = f, t, history.StepDay
		if to.Sub(from) <= 3*24*time.Hour {
			step = history.StepHour
		}
	} else if m := q.Get("month"); m != "" {
		first, err := time.ParseInLocation("2006-01", m, loc)
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "month: like 2026-11")
			return
		}
		from, to, step = first, first.AddDate(0, 1, 0), history.StepDay
	} else {
		nextHour := now.Truncate(time.Hour).Add(time.Hour)
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
		switch q.Get("range") {
		case "", "24h":
			from, to = nextHour.Add(-24*time.Hour), nextHour
		case "7d":
			from, to = nextHour.Add(-7*24*time.Hour), nextHour
		case "30d":
			from, to, step = today.AddDate(0, 0, -29), today.AddDate(0, 0, 1), history.StepDay
		default:
			httpx.WriteError(w, http.StatusBadRequest, "range: 24h, 7d or 30d")
			return
		}
	}
	rep, err := s.stats(r).Uptime(r.Context(), from, to, step, loc, s.Now())
	if err != nil {
		s.queryError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, rep)
}

// dateRange reads from / to (YYYY-MM-DD, both days included) in the web's
// time zone: [from 00:00, the day after to 00:00).
func (s *Server) dateRange(r *http.Request) (from, to time.Time, err error) {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	v := r.URL.Query()
	if from, err = time.ParseInLocation(time.DateOnly, v.Get("from"), loc); err != nil {
		return from, to, fmt.Errorf("%w: from must be YYYY-MM-DD", history.ErrBadQuery)
	}
	if to, err = time.ParseInLocation(time.DateOnly, v.Get("to"), loc); err != nil {
		return from, to, fmt.Errorf("%w: to must be YYYY-MM-DD", history.ErrBadQuery)
	}
	to = to.AddDate(0, 0, 1)
	if !to.After(from) {
		return from, to, fmt.Errorf("%w: to is before from", history.ErrBadQuery)
	}
	if to.Sub(from) > 4*366*24*time.Hour {
		return from, to, fmt.Errorf("%w: at most 4 years", history.ErrBadQuery)
	}
	return from, to, nil
}

// eventsQuery reads the events parameters: the period, a client, and quiet=1
// to fold quick reconnects (the online / offline messages' quiet time).
func (s *Server) eventsQuery(r *http.Request) (from, to time.Time, name string, quiet time.Duration, err error) {
	if from, to, err = s.dateRange(r); err != nil {
		return
	}
	v := r.URL.Query()
	name = v.Get("name")
	if len(name) > 128 {
		err = fmt.Errorf("%w: invalid name", history.ErrBadQuery)
		return
	}
	if v.Get("quiet") == "1" {
		ps, _ := s.presenceSettings(r.Context())
		quiet = time.Duration(ps.GraceMinutes) * time.Minute
	}
	return
}

// handleEvents is GET /api/v1/servers/{server}/history/events: ups and
// downs, newest first, a page at a time.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	from, to, name, quiet, err := s.eventsQuery(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ev, total, err := s.stats(r).Events(r.Context(), from, to, name, quiet, max(offset, 0), limit)
	if err != nil {
		s.queryError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"total": total, "events": ev})
}

// handleEventsCSV is the same list as CSV, all of it, oldest first.
func (s *Server) handleEventsCSV(w http.ResponseWriter, r *http.Request) {
	from, to, name, quiet, err := s.eventsQuery(r)
	if err != nil {
		s.queryError(w, err)
		return
	}
	st := s.stats(r)
	var all []history.Event
	for off := 0; ; off += 1000 {
		page, total, err := st.Events(r.Context(), from, to, name, quiet, off, 1000)
		if err != nil {
			s.queryError(w, err)
			return
		}
		all = append(all, page...)
		if off+1000 >= total {
			break
		}
	}
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="uptime-%s-%s.csv"`, from.Format(time.DateOnly), to.AddDate(0, 0, -1).Format(time.DateOnly)))
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "client", "event", "from", "was_up_seconds", "quick_reconnects"})
	for i := len(all) - 1; i >= 0; i-- {
		e := all[i]
		kind, lasted := "down", strconv.FormatInt(e.Lasted, 10)
		if e.Up {
			kind, lasted = "up", ""
		}
		_ = cw.Write([]string{e.Time.In(loc).Format("2006-01-02 15:04:05"), e.Name, kind, e.From, lasted, strconv.Itoa(e.Merged)})
	}
	cw.Flush()
}
