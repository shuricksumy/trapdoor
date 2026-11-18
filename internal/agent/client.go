package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
)

// Client calls the agent API. It is used by the web process.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient returns a client for url ("http://host:port" or "unix:/path").
func NewClient(url, token string, timeout time.Duration) *Client {
	c := &Client{
		base:  strings.TrimRight(url, "/"),
		token: token,
		http:  &http.Client{Timeout: timeout},
	}
	if path, ok := strings.CutPrefix(url, "unix:"); ok {
		c.base = "http://agent"
		c.http.Transport = &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
		}
	}
	return c
}

// Info returns basic information about the agent.
func (c *Client) Info(ctx context.Context) (api.AgentInfo, error) {
	var out api.AgentInfo
	err := c.do(ctx, http.MethodGet, "/v1/info", &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set(api.HeaderToken, c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var e api.Error
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return fmt.Errorf("agent %s %s: %s (%d)", method, path, e.Error, resp.StatusCode)
		}
		return fmt.Errorf("agent %s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
