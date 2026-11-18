package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
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

// APIError is a non-2xx answer from the agent.
type APIError struct {
	Status  int    // HTTP status code
	Message string // agent's error message
}

func (e *APIError) Error() string { return fmt.Sprintf("agent: %s (%d)", e.Message, e.Status) }

// IsStatus reports whether err is an APIError with the given status code.
func IsStatus(err error, status int) bool {
	var e *APIError
	return errors.As(err, &e) && e.Status == status
}

// ListCerts returns all certificate entries.
func (c *Client) ListCerts(ctx context.Context) ([]api.Cert, error) {
	var out []api.Cert
	err := c.doJSON(ctx, http.MethodGet, "/v1/certs", nil, &out)
	return out, err
}

// GetCert returns the current certificate for name.
func (c *Client) GetCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodGet, "/v1/certs/"+url.PathEscape(name), nil, &out)
	return out, err
}

// IssueCert issues a new certificate.
func (c *Client) IssueCert(ctx context.Context, req api.IssueCertRequest) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, "/v1/certs", req, &out)
	return out, err
}

// RevokeCert revokes the current certificate for name.
func (c *Client) RevokeCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, "/v1/certs/"+url.PathEscape(name)+"/revoke", nil, &out)
	return out, err
}

// UnrevokeCert restores a revoked certificate.
func (c *Client) UnrevokeCert(ctx context.Context, name string) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, "/v1/certs/"+url.PathEscape(name)+"/unrevoke", nil, &out)
	return out, err
}

// RenewCert issues a new certificate with the same key.
func (c *Client) RenewCert(ctx context.Context, name string, validityDays int) (api.Cert, error) {
	var out api.Cert
	err := c.doJSON(ctx, http.MethodPost, "/v1/certs/"+url.PathEscape(name)+"/renew",
		api.RenewCertRequest{ValidityDays: validityDays}, &out)
	return out, err
}

// RemoveCert revokes (if needed) and deletes the key material for name.
func (c *Client) RemoveCert(ctx context.Context, name string) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/certs/"+url.PathEscape(name), nil, nil)
}

// Profile returns the .ovpn client profile for name.
func (c *Client) Profile(ctx context.Context, name string) ([]byte, error) {
	var out []byte
	err := c.doJSON(ctx, http.MethodGet, "/v1/certs/"+url.PathEscape(name)+"/ovpn", nil, &out)
	return out, err
}

// GenCRL regenerates the CRL.
func (c *Client) GenCRL(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/crl", nil, nil)
}

// Info returns basic information about the agent.
func (c *Client) Info(ctx context.Context) (api.AgentInfo, error) {
	var out api.AgentInfo
	err := c.doJSON(ctx, http.MethodGet, "/v1/info", nil, &out)
	return out, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(api.HeaderToken, c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("agent %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 300 {
		var e api.Error
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if json.Unmarshal(b, &e) != nil || e.Error == "" {
			e.Error = http.StatusText(resp.StatusCode)
		}
		return &APIError{Status: resp.StatusCode, Message: e.Error}
	}
	if out == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
