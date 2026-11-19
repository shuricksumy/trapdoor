package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
)

func TestJoin(t *testing.T) {
	old := joinRetry
	joinRetry = 10 * time.Millisecond
	t.Cleanup(func() { joinRetry = old })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	req := api.JoinRequest{Token: "tok", Name: "wg", URL: "https://node:9443", Fingerprint: "sha256:00", AgentToken: "a"}

	// The web UI answers 503 twice (starting), then takes the node.
	var calls atomic.Int32
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got api.JoinRequest
		_ = json.NewDecoder(r.Body).Decode(&got)
		if r.URL.Path != "/api/v1/nodes/join" || got != req {
			t.Errorf("join request %s %+v", r.URL.Path, got)
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(web.Close)
	if err := Join(ctx, log, web.URL+"/", req); err != nil || calls.Load() != 3 {
		t.Fatalf("join = %v after %d calls", err, calls.Load())
	}

	// A refusal is final.
	calls.Store(0)
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid, used or expired join token"}`))
	}))
	t.Cleanup(refuse.Close)
	var je *JoinError
	if err := Join(ctx, log, refuse.URL, req); !errors.As(err, &je) || je.Status != 401 || calls.Load() != 1 {
		t.Errorf("refused join = %v after %d calls", err, calls.Load())
	}

	// Nobody answers: it keeps trying until stopped.
	cctx, cancel := context.WithTimeout(ctx, 60*time.Millisecond)
	defer cancel()
	if err := Join(cctx, log, "http://127.0.0.1:1", req); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("unreachable = %v", err)
	}
}
