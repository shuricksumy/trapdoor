package web

import (
	"context"
	"encoding/csv"
	"net/http"
	"strconv"
	"time"

	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// The audit log: every change, sign-in and failed sign-in, with who, how
// they signed in and from where; kept in the database (two years) and
// in the log.

// auditAs writes a line for someone who may not be signed in (yet).
func (s *Server) auditAs(r *http.Request, user, via, action, target string) {
	ip := s.clientIP(r)
	server := r.PathValue("server")
	s.Log.Info("audit", "server", server, "action", action, "target", target, "by", user, "via", via, "ip", ip)
	if s.Stats == nil || s.Stats.DB == nil {
		return
	}
	e := store.AuditEntry{Time: s.Now().UTC(), Username: user, Via: via, IP: ip, Action: action, Server: server, Target: target}
	if err := s.Stats.DB.AddAudit(context.WithoutCancel(r.Context()), e); err != nil {
		s.Log.Warn("audit: not stored", "err", err)
	}
}

// AuditPage is GET /api/v1/audit.
type AuditPage struct {
	Total   int                `json:"total"`
	Entries []store.AuditEntry `json:"entries"`
	Users   []string           `json:"users"` // every name in the log, for the filter
}

func (s *Server) registerAuditRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit", s.adminOnly(s.handleAudit))
	mux.HandleFunc("GET /api/v1/audit.csv", s.adminOnly(s.handleAuditCSV))
}

// auditFilter: ?from=YYYY-MM-DD&to=YYYY-MM-DD (both days included, in
// TD_TZ), user, action (an action or its group), q (search).
func (s *Server) auditFilter(r *http.Request) (store.AuditFilter, error) {
	q := r.URL.Query()
	f := store.AuditFilter{User: q.Get("user"), Action: q.Get("action"), Search: q.Get("q")}
	if v := q.Get("from"); v != "" {
		t, err := time.ParseInLocation(time.DateOnly, v, s.Location)
		if err != nil {
			return f, err
		}
		f.From = t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.ParseInLocation(time.DateOnly, v, s.Location)
		if err != nil {
			return f, err
		}
		f.To = t.AddDate(0, 0, 1)
	}
	return f, nil
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	f, err := s.auditFilter(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dates: YYYY-MM-DD")
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 50
	}
	entries, total, err := s.Stats.DB.Audit(r.Context(), f, max(offset, 0), limit)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	users, err := s.Stats.DB.AuditUsers(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if users == nil {
		users = []string{}
	}
	httpx.WriteJSON(w, http.StatusOK, AuditPage{Total: total, Entries: entries, Users: users})
}

func (s *Server) handleAuditCSV(w http.ResponseWriter, r *http.Request) {
	f, err := s.auditFilter(r)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "dates: YYYY-MM-DD")
		return
	}
	entries, _, err := s.Stats.DB.Audit(r.Context(), f, 0, 100000)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="trapdoor-audit.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "user", "via", "address", "action", "server", "target"})
	for i := len(entries) - 1; i >= 0; i-- { // oldest first
		e := entries[i]
		_ = cw.Write([]string{e.Time.In(s.Location).Format(time.RFC3339), e.Username, e.Via, e.IP, e.Action, e.Server, csvSafe(e.Target)})
	}
	cw.Flush()
}

// csvSafe keeps a spreadsheet from reading a value as a formula.
func csvSafe(v string) string {
	if v != "" && (v[0] == '=' || v[0] == '+' || v[0] == '-' || v[0] == '@') {
		return "'" + v
	}
	return v
}
