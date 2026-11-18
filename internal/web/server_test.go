package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/store"
)

// fakeAgent overrides Info and live sessions; certificate calls go to a
// real agent with a real PKI.
type fakeAgent struct {
	*agent.Client
	info     api.AgentInfo
	err      error
	sessions []api.Session
}

func (f *fakeAgent) Info(context.Context) (api.AgentInfo, error) { return f.info, f.err }
func (f *fakeAgent) Server(id string) AgentClient {
	return &fakeServer{Client: f.Client.Server(id), f: f}
}

type fakeServer struct {
	*agent.Client
	f *fakeAgent
}

func (s *fakeServer) Sessions(context.Context) (api.Sessions, error) {
	return api.Sessions{Sessions: s.f.sessions}, s.f.err
}

const token = "test-token-0123456789"

var now = time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)

type testEnv struct {
	t     *testing.T
	h     http.Handler
	agent *fakeAgent
	col   *history.Collector
	clock time.Time
	auth  *auth.Service
}

func newTestEnv(t *testing.T, apiToken string) *testEnv {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "td.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	p, err := pki.Init(filepath.Join(t.TempDir(), "pki"), pki.InitOptions{}, pki.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ag := httptest.NewServer(agent.NewHost("agent-secret", log, agent.New(config.Agent{Token: "agent-secret", OpenVPNDir: t.TempDir(), CCDDir: t.TempDir(), TOTPIssuer: "trapdoor", MgmtAddr: "127.0.0.1:1"}, log, p)).Handler())
	t.Cleanup(ag.Close)
	e := &testEnv{t: t, clock: now, agent: &fakeAgent{
		Client: agent.NewClient(ag.URL, "agent-secret", 5*time.Second),
		info:   api.AgentInfo{Hostname: "vpn1", Servers: []api.ServerInfo{{ID: "openvpn", Type: api.TypeOpenVPN}}}}}
	e.col = &history.Collector{DB: db, Source: e.agent.Server("openvpn"), Agent: "openvpn", Log: log,
		Now: func() time.Time { return e.clock }}
	e.auth = auth.New(db, auth.Options{})
	e.h = New(Deps{Agent: e.agent, Stats: &history.Stats{DB: db},
		Auth: e.auth, Limiter: &auth.Limiter{MaxFailures: 3, MaxUserFailures: 5},
		APIToken: apiToken, CookieSecure: true,
		TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		Location:       time.UTC, Log: log, Now: func() time.Time { return now }}).Handler()
	return e
}

func (e *testEnv) get(path, tok string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) poll(at time.Time, sessions ...api.Session) {
	e.t.Helper()
	e.clock = at
	e.agent.sessions = sessions
	if err := e.col.Poll(context.Background()); err != nil {
		e.t.Fatal(err)
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return v
}

func TestTokenRequired(t *testing.T) {
	e := newTestEnv(t, token)
	if rec := e.get("/healthz", ""); rec.Code != http.StatusOK {
		t.Errorf("healthz = %d", rec.Code)
	}
	for _, p := range []string{"/api/v1/status", "/api/v1/servers/openvpn/stats", "/api/v1/servers/openvpn/stats.csv", "/api/v1/servers/openvpn/history/sessions", "/api/v1/servers/openvpn/sessions"} {
		for _, tok := range []string{"", "wrong"} {
			if rec := e.get(p, tok); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s with %q: %d, want 401", p, tok, rec.Code)
			}
		}
	}
	if rec := e.get("/api/v1/status", token); rec.Code != http.StatusOK {
		t.Errorf("status with token = %d", rec.Code)
	}

	off := newTestEnv(t, "")
	if rec := off.get("/api/v1/status", "anything"); rec.Code != http.StatusUnauthorized {
		t.Errorf("bearer accepted with no API token configured: %d", rec.Code)
	}
}

func TestStatus(t *testing.T) {
	e := newTestEnv(t, token)
	st := decode[api.Status](t, e.get("/api/v1/status", token))
	if !st.AgentOK || st.Agent.Hostname != "vpn1" {
		t.Errorf("status = %+v", st)
	}
	e.agent.err = errors.New("connection refused")
	st = decode[api.Status](t, e.get("/api/v1/status", token))
	if st.AgentOK || st.AgentError == "" {
		t.Errorf("agent down: %+v", st)
	}
	if rec := e.get("/api/v1/servers/openvpn/sessions", token); rec.Code != http.StatusBadGateway {
		t.Errorf("live sessions with agent down = %d, want 502", rec.Code)
	}
}

func TestStatsEndpoints(t *testing.T) {
	e := newTestEnv(t, token)
	day := time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	s := func(in, out int64) api.Session {
		return api.Session{ClientID: 1, Name: "alice", RealAddress: "1.2.3.4:1", ConnectedSince: day, BytesReceived: in, BytesSent: out}
	}
	e.poll(day, s(0, 0))
	e.poll(day.Add(30*time.Second), s(1000, 5000))
	e.poll(day.Add(time.Minute))

	// Default range = this month (now is 2026-11-18).
	rep := decode[history.Report](t, e.get("/api/v1/servers/openvpn/stats", token))
	if rep.From != "2026-11-01" || rep.To != "2026-11-18" || rep.Total.Upload != 1000 || rep.Total.Download != 5000 {
		t.Errorf("default report = %+v", rep.Total)
	}
	if len(rep.Users) != 1 || rep.Users[0].Name != "alice" || rep.Users[0].Sessions != 1 {
		t.Errorf("users = %+v", rep.Users)
	}

	rep = decode[history.Report](t, e.get("/api/v1/servers/openvpn/stats?preset=this_year&group=month&name=alice", token))
	if len(rep.Series) != 11 || rep.Series[10].Period != "2026-11" || rep.Series[10].Download != 5000 {
		t.Errorf("monthly = %+v", rep.Series)
	}
	rep = decode[history.Report](t, e.get("/api/v1/servers/openvpn/stats?from=2026-10-01&to=2026-10-31", token))
	if rep.Total.Upload != 0 || len(rep.Series) != 31 {
		t.Errorf("october = %+v (%d points)", rep.Total, len(rep.Series))
	}

	for _, bad := range []string{"?from=2026-11-01", "?from=x&to=y", "?preset=forever", "?group=week",
		"?from=2026-11-05&to=2026-11-01", "?name=../x"} {
		if rec := e.get("/api/v1/servers/openvpn/stats"+bad, token); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", bad, rec.Code)
		}
	}

	rec := e.get("/api/v1/servers/openvpn/stats.csv?from=2026-11-01&to=2026-11-30", token)
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "trapdoor-2026-11-01_2026-11-30.csv") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	records, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"period", "name", "upload_bytes", "download_bytes", "estimated"},
		{"2026-11-03", "alice", "1000", "5000", "false"},
	}
	if len(records) != 2 || strings.Join(records[1], ",") != strings.Join(want[1], ",") {
		t.Errorf("csv = %v", records)
	}

	list := decode[[]history.Session](t, e.get("/api/v1/servers/openvpn/history/sessions?preset=this_month&name=alice", token))
	if len(list) != 1 || list[0].Upload != 1000 || list[0].DurationSec != 30 || list[0].DisconnectedAt == nil {
		t.Errorf("history sessions = %+v", list)
	}
}
