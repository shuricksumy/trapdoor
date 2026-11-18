package web

import (
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/shuricksumy/tunnelward/internal/history"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/pki"
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
	rep, err := s.Stats.Report(r.Context(), q)
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
	rep, err := s.Stats.Report(r.Context(), q)
	if err != nil {
		s.queryError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="tunnelward-%s_%s.csv"`, rep.From, rep.To))
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
	list, err := s.Stats.Sessions(r.Context(), q.Name, from, to, limit)
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
