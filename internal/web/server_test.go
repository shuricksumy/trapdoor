package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/shuricksumy/tunnelward/internal/api"
)

type fakeAgent struct {
	info api.AgentInfo
	err  error
}

func (f fakeAgent) Info(context.Context) (api.AgentInfo, error) { return f.info, f.err }

func getStatus(t *testing.T, a AgentClient) api.Status {
	t.Helper()
	h := New(a, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d", rec.Code)
	}
	var st api.Status
	if err := json.NewDecoder(rec.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestStatusAgentOK(t *testing.T) {
	st := getStatus(t, fakeAgent{info: api.AgentInfo{Hostname: "vpn1"}})
	if !st.AgentOK || st.Agent == nil || st.Agent.Hostname != "vpn1" {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestStatusAgentDown(t *testing.T) {
	st := getStatus(t, fakeAgent{err: errors.New("connection refused")})
	if st.AgentOK || st.AgentError == "" {
		t.Fatalf("unexpected status: %+v", st)
	}
}
