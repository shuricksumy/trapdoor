// Package mgmt is a client for the OpenVPN management interface.
//
// It opens a short-lived connection per call (the interface serves one
// client at a time, so calls are serialized), skips asynchronous ">..."
// notifications, and parses "status 3" by its HEADER lines rather than fixed
// column positions, so it works across OpenVPN 2.4–2.6 output variants.
package mgmt

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrUnavailable wraps connection failures to the management interface.
var ErrUnavailable = errors.New("openvpn management interface unavailable")

// ErrCommand is returned when OpenVPN answers "ERROR: ...".
var ErrCommand = errors.New("openvpn management command failed")

// Client talks to one management interface.
type Client struct {
	// Addr is "host:port" or "unix:/path".
	Addr string
	// Password for "management ... pw-file" setups; empty if none.
	Password string
	// Timeout per call; default 5s.
	Timeout time.Duration

	mu sync.Mutex
}

// Session is one connected client from CLIENT_LIST.
type Session struct {
	ClientID       int64     `json:"client_id"`
	PeerID         int64     `json:"peer_id"`
	Name           string    `json:"name"` // common name
	Username       string    `json:"username,omitempty"`
	RealAddress    string    `json:"real_address"`
	VirtualAddress string    `json:"virtual_address,omitempty"`
	VirtualIPv6    string    `json:"virtual_ipv6,omitempty"`
	BytesReceived  int64     `json:"bytes_received"`
	BytesSent      int64     `json:"bytes_sent"`
	ConnectedSince time.Time `json:"connected_since"`
	Cipher         string    `json:"cipher,omitempty"`
}

// Status is the parsed result of "status 3".
type Status struct {
	Title    string    `json:"title"`
	Time     time.Time `json:"time"`
	Sessions []Session `json:"sessions"`
}

// LoadStats is the result of "load-stats".
type LoadStats struct {
	Clients  int64 `json:"clients"`
	BytesIn  int64 `json:"bytes_in"`
	BytesOut int64 `json:"bytes_out"`
}

// Status returns the connected clients.
func (c *Client) Status(ctx context.Context) (Status, error) {
	lines, err := c.exec(ctx, "status 3", true)
	if err != nil {
		return Status{}, err
	}
	return parseStatus(lines)
}

// LoadStats returns server-wide counters.
func (c *Client) LoadStats(ctx context.Context) (LoadStats, error) {
	lines, err := c.exec(ctx, "load-stats", false)
	if err != nil {
		return LoadStats{}, err
	}
	return parseLoadStats(lines[0])
}

// Version returns the "OpenVPN Version: ..." line.
func (c *Client) Version(ctx context.Context) (string, error) {
	lines, err := c.exec(ctx, "version", true)
	if err != nil {
		return "", err
	}
	for _, l := range lines {
		if v, ok := strings.CutPrefix(l, "OpenVPN Version: "); ok {
			return v, nil
		}
	}
	return "", errors.New("no version in response")
}

// KillClient disconnects one session by client ID.
func (c *Client) KillClient(ctx context.Context, clientID int64) error {
	_, err := c.exec(ctx, "client-kill "+strconv.FormatInt(clientID, 10), false)
	return err
}

// KillName disconnects all sessions with the given common name and returns
// how many were killed (0 when none was connected).
func (c *Client) KillName(ctx context.Context, name string) (int, error) {
	if name == "" || strings.ContainsAny(name, " \t\r\n\"'\\") {
		return 0, fmt.Errorf("invalid common name %q", name)
	}
	lines, err := c.exec(ctx, "kill "+name, false)
	if errors.Is(err, ErrCommand) && strings.Contains(err.Error(), "not found") {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	// "SUCCESS: common name 'alice' found, 2 client(s) killed"
	f := strings.Fields(lines[0])
	for i, w := range f {
		if strings.HasPrefix(w, "client(s)") && i > 0 {
			n, _ := strconv.Atoi(f[i-1])
			return n, nil
		}
	}
	return 1, nil
}

// exec runs one command. multiline commands end with "END"; the others
// answer with a single SUCCESS:/ERROR: line.
func (c *Client) exec(ctx context.Context, cmd string, multiline bool) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	network, addr := "tcp", c.Addr
	if p, ok := strings.CutPrefix(c.Addr, "unix:"); ok {
		network, addr = "unix", p
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	r := bufio.NewReader(conn)
	if c.Password != "" {
		if err := c.login(conn, r); err != nil {
			return nil, err
		}
	}
	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}

	var out []string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("%w: reading %q: %v", ErrUnavailable, cmd, err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, ">"):
			continue // async notification (>INFO, >LOG, >BYTECOUNT, ...)
		case strings.HasPrefix(line, "ERROR:"):
			return nil, fmt.Errorf("%w: %s: %s", ErrCommand, cmd, strings.TrimSpace(strings.TrimPrefix(line, "ERROR:")))
		case multiline && line == "END":
			_, _ = fmt.Fprint(conn, "quit\n")
			return out, nil
		case !multiline && strings.HasPrefix(line, "SUCCESS:"):
			_, _ = fmt.Fprint(conn, "quit\n")
			return []string{strings.TrimSpace(strings.TrimPrefix(line, "SUCCESS:"))}, nil
		case multiline:
			out = append(out, line)
		}
	}
}

func (c *Client) login(conn net.Conn, r *bufio.Reader) error {
	// The prompt "ENTER PASSWORD:" has no trailing newline.
	buf := make([]byte, 0, 64)
	for !strings.Contains(string(buf), "ENTER PASSWORD:") {
		b, err := r.ReadByte()
		if err != nil {
			return fmt.Errorf("%w: waiting for password prompt: %v", ErrUnavailable, err)
		}
		buf = append(buf, b)
		if len(buf) > 4096 {
			return fmt.Errorf("%w: no password prompt", ErrUnavailable)
		}
	}
	if _, err := fmt.Fprintf(conn, "%s\n", c.Password); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("%w: after password: %v", ErrUnavailable, err)
		}
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "SUCCESS:"):
			return nil
		case strings.HasPrefix(line, "ERROR:"):
			return fmt.Errorf("%w: management password rejected", ErrCommand)
		}
	}
}

func parseStatus(lines []string) (Status, error) {
	var st Status
	cols := map[string]int{}
	st.Sessions = []Session{}
	for _, line := range lines {
		f := strings.Split(line, "\t")
		switch f[0] {
		case "TITLE":
			if len(f) > 1 {
				st.Title = f[1]
			}
		case "TIME":
			if len(f) > 2 {
				st.Time = unix(f[2])
			}
		case "HEADER":
			if len(f) > 1 && f[1] == "CLIENT_LIST" {
				for i, name := range f[2:] {
					cols[name] = i + 1 // index into a CLIENT_LIST row
				}
			}
		case "CLIENT_LIST":
			if len(cols) == 0 {
				return st, errors.New("CLIENT_LIST before its HEADER (is this status 3?)")
			}
			get := func(name string) string {
				if i, ok := cols[name]; ok && i < len(f) {
					return f[i]
				}
				return ""
			}
			s := Session{
				Name:           get("Common Name"),
				RealAddress:    get("Real Address"),
				VirtualAddress: get("Virtual Address"),
				VirtualIPv6:    get("Virtual IPv6 Address"),
				BytesReceived:  atoi(get("Bytes Received")),
				BytesSent:      atoi(get("Bytes Sent")),
				ConnectedSince: unix(get("Connected Since (time_t)")),
				Username:       get("Username"),
				ClientID:       atoi(get("Client ID")),
				PeerID:         atoi(get("Peer ID")),
				Cipher:         get("Data Channel Cipher"),
			}
			if s.Username == "UNDEF" {
				s.Username = ""
			}
			st.Sessions = append(st.Sessions, s)
		}
	}
	return st, nil
}

func parseLoadStats(line string) (LoadStats, error) {
	// "nclients=1,bytesin=6152,bytesout=4386"
	var ls LoadStats
	for _, kv := range strings.Split(line, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return ls, fmt.Errorf("bad load-stats field %q", kv)
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return ls, fmt.Errorf("bad load-stats value %q", kv)
		}
		switch k {
		case "nclients":
			ls.Clients = n
		case "bytesin":
			ls.BytesIn = n
		case "bytesout":
			ls.BytesOut = n
		}
	}
	return ls, nil
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func unix(s string) time.Time {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n == 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}
