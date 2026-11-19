// Package mail sends plain-text emails with attachments over SMTP
// (implicit TLS, STARTTLS or, for a relay on the same machine, plain).
package mail

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Security of the connection to the SMTP server.
const (
	SecurityTLS      = "tls"      // implicit TLS, usually port 465
	SecuritySTARTTLS = "starttls" // usually port 587
	SecurityNone     = "none"     // only for a relay on the same machine or network
)

// Config is an SMTP account.
type Config struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Security string `json:"security"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// From is the sender, e.g. "trapdoor <vpn@example.com>".
	From string `json:"from"`
	// InsecureSkipVerify accepts any server certificate (self-signed relays).
	InsecureSkipVerify bool `json:"insecure_skip_verify,omitempty"`
}

// Attachment is a file sent with a message.
type Attachment struct {
	Name        string
	ContentType string
	Data        []byte
}

// Message is a plain-text email.
type Message struct {
	To          []string
	Subject     string
	Text        string
	Attachments []Attachment
}

// Validate checks a configuration before it is saved or used.
func (c Config) Validate() error {
	switch {
	case c.Host == "" || strings.ContainsAny(c.Host, " /\r\n"):
		return errors.New("host: the SMTP server's name, e.g. smtp.example.com")
	case c.Port < 1 || c.Port > 65535:
		return errors.New("port: 1-65535 (465 for TLS, 587 for STARTTLS)")
	case c.Security != SecurityTLS && c.Security != SecuritySTARTTLS && c.Security != SecurityNone:
		return errors.New("security: tls, starttls or none")
	case c.Password != "" && c.Username == "":
		return errors.New("username: needed with a password")
	}
	if _, err := mail.ParseAddress(c.From); err != nil {
		return fmt.Errorf("from: %w", err)
	}
	return nil
}

// ParseAddresses checks a list of recipients ("a@example.com, b@example.com").
func ParseAddresses(list []string) ([]string, error) {
	var out []string
	for _, a := range list {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		addr, err := mail.ParseAddress(a)
		if err != nil {
			return nil, fmt.Errorf("%q: not an email address", a)
		}
		out = append(out, addr.Address)
	}
	return out, nil
}

// Dialer is how Send connects (replaced in tests).
var Dialer = &net.Dialer{Timeout: 15 * time.Second}

// Send delivers m with the account c.
func Send(ctx context.Context, c Config, m Message) error {
	if err := c.Validate(); err != nil {
		return err
	}
	to, err := ParseAddresses(m.To)
	if err != nil {
		return err
	}
	if len(to) == 0 {
		return errors.New("no recipient")
	}
	from, _ := mail.ParseAddress(c.From)
	body, err := build(from, to, m, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	tlsConf := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureSkipVerify} //nolint:gosec // opt-in for self-signed relays
	conn, err := Dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if c.Security == SecurityTLS {
		tc := tls.Client(conn, tlsConf)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return err
		}
		conn = tc
	}
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = cl.Close() }()
	if err := cl.Hello("localhost"); err != nil {
		return err
	}
	if c.Security == SecuritySTARTTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tlsConf); err != nil {
			return err
		}
	}
	if c.Username != "" {
		// PLAIN only over TLS (net/smtp refuses it otherwise, except to localhost).
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("login: %w", err)
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return err
	}
	for _, t := range to {
		if err := cl.Rcpt(t); err != nil {
			return fmt.Errorf("%s: %w", t, err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return cl.Quit()
}

// build renders the message: text/plain, or multipart/mixed with attachments.
func build(from *mail.Address, to []string, m Message, now time.Time) ([]byte, error) {
	if strings.ContainsAny(m.Subject, "\r\n") {
		return nil, errors.New("subject: one line")
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", m.Subject))
	h("Date", now.Format(time.RFC1123Z))
	h("Message-ID", "<"+randHex(12)+"@"+domain(from.Address)+">")
	h("MIME-Version", "1.0")
	h("Auto-Submitted", "auto-generated")
	text := base64Lines([]byte(strings.ReplaceAll(m.Text, "\n", "\r\n")))
	if len(m.Attachments) == 0 {
		h("Content-Type", `text/plain; charset="utf-8"`)
		h("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n" + text)
		return b.Bytes(), nil
	}
	boundary := "td-" + randHex(16)
	h("Content-Type", `multipart/mixed; boundary="`+boundary+`"`)
	b.WriteString("\r\n--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" + text)
	for _, a := range m.Attachments {
		if strings.ContainsAny(a.Name, "\"\r\n/\\") || a.Name == "" {
			return nil, fmt.Errorf("attachment name %q", a.Name)
		}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + ct + "; name=\"" + a.Name + "\"\r\n")
		b.WriteString("Content-Disposition: attachment; filename=\"" + a.Name + "\"\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n" + base64Lines(a.Data))
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

func base64Lines(data []byte) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.String()
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func domain(addr string) string {
	if _, d, ok := strings.Cut(addr, "@"); ok && d != "" {
		return d
	}
	return "trapdoor.invalid"
}
