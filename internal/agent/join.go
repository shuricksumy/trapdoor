package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
)

// A node adds itself to a web UI: at start it sends a join token, its name,
// its address and its certificate fingerprint and agent token to
// <join url>/api/v1/nodes/join. The web UI connects back with the pinned
// certificate before it saves the node. Joining again with the same
// certificate (every restart) needs no new token.

// joinRetry is how long to wait between attempts while the web UI does not
// answer yet (it may start after the node).
var joinRetry = 10 * time.Second

// JoinError is an answer from the web UI that retrying does not change.
type JoinError struct {
	Status  int
	Message string
}

func (e *JoinError) Error() string { return fmt.Sprintf("join refused (%d): %s", e.Status, e.Message) }

// Join adds this node to the web UI at joinURL; it retries until the web UI
// answers or ctx ends. A refusal (bad token, name taken) is not retried.
func Join(ctx context.Context, log *slog.Logger, joinURL string, req api.JoinRequest) error {
	body, _ := json.Marshal(req)
	url := strings.TrimRight(joinURL, "/") + "/api/v1/nodes/join"
	hc := &http.Client{Timeout: 30 * time.Second}
	for attempt := 1; ; attempt++ {
		err := func() error {
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return err
			}
			r.Header.Set("Content-Type", "application/json")
			resp, err := hc.Do(r)
			if err != nil {
				return err
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode/100 == 2 {
				return nil
			}
			var e api.Error
			_ = json.NewDecoder(resp.Body).Decode(&e)
			je := &JoinError{Status: resp.StatusCode, Message: e.Error}
			if resp.StatusCode >= 500 {
				return fmt.Errorf("%w", je) // the web UI may be starting: retry
			}
			return je
		}()
		if err == nil {
			log.Info("joined the web UI", "url", joinURL, "node", req.Name)
			return nil
		}
		if je, ok := err.(*JoinError); ok {
			log.Error("the web UI refused this node; create a new join token", "url", joinURL, "err", je.Message)
			return je
		}
		if attempt == 1 || attempt%30 == 0 {
			log.Info("web UI not reachable yet, trying again", "url", joinURL, "err", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(joinRetry):
		}
	}
}
