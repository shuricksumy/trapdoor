package mail

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTP is a minimal SMTP server: EHLO, STARTTLS, AUTH PLAIN, MAIL, RCPT,
// DATA, QUIT. It keeps what it received.
type fakeSMTP struct {
	ln       net.Listener
	tls      *tls.Config
	implicit bool // TLS from the first byte
	user     string
	pass     string

	mu   sync.Mutex
	got  []received
	auth []string
}

type received struct {
	from string
	to   []string
	data string
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newFake(t *testing.T, implicit bool) *fakeSMTP {
	t.Helper()
	f := &fakeSMTP{tls: &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}, implicit: implicit, user: "vpn", pass: "secret"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if implicit {
		ln = tls.NewListener(ln, f.tls)
	}
	f.ln = ln
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) port() int { return f.ln.Addr().(*net.TCPAddr).Port }

func (f *fakeSMTP) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	r, w := bufio.NewReader(c), c
	say := func(s string) { _, _ = io.WriteString(w, s+"\r\n") }
	say("220 fake ESMTP")
	secure := f.implicit
	var cur received
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
		switch cmd {
		case "EHLO", "HELO":
			ext := []string{"250-fake", "250-AUTH PLAIN", "250-8BITMIME"}
			if !secure {
				ext = append(ext, "250-STARTTLS")
			}
			for i, e := range ext {
				if i == len(ext)-1 {
					e = "250 " + e[4:]
				}
				say(e)
			}
		case "STARTTLS":
			say("220 go ahead")
			tc := tls.Server(c, f.tls)
			if err := tc.Handshake(); err != nil {
				return
			}
			c, r, w, secure = tc, bufio.NewReader(tc), tc, true
		case "AUTH":
			b, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
			parts := strings.Split(string(b), "\x00")
			f.mu.Lock()
			f.auth = append(f.auth, strings.Join(parts, "|"))
			f.mu.Unlock()
			if len(parts) == 3 && parts[1] == f.user && parts[2] == f.pass {
				say("235 ok")
			} else {
				say("535 bad login")
			}
		case "MAIL":
			cur = received{from: line}
			say("250 ok")
		case "RCPT":
			cur.to = append(cur.to, line)
			say("250 ok")
		case "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			cur.data = b.String()
			f.mu.Lock()
			f.got = append(f.got, cur)
			f.mu.Unlock()
			say("250 queued")
		case "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestSend(t *testing.T) {
	ctx := context.Background()
	msg := Message{To: []string{"Admin <admin@example.com>", "phone@example.com"}, Subject: "Ваш VPN: laptop",
		Text: "Hello,\nyour profile is attached.", Attachments: []Attachment{{Name: "laptop.ovpn", ContentType: "application/x-openvpn-profile", Data: []byte("client\nremote vpn.example.com 1194\n")}}}
	for _, sec := range []string{SecuritySTARTTLS, SecurityTLS, SecurityNone} {
		t.Run(sec, func(t *testing.T) {
			f := newFake(t, sec == SecurityTLS)
			c := Config{Host: "127.0.0.1", Port: f.port(), Security: sec, Username: "vpn", Password: "secret", From: "trapdoor <vpn@example.com>", InsecureSkipVerify: true}
			if err := Send(ctx, c, msg); err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.got) != 1 || len(f.got[0].to) != 2 || !strings.Contains(f.got[0].from, "vpn@example.com") {
				t.Fatalf("received %+v", f.got)
			}
			if len(f.auth) != 1 || f.auth[0] != "|vpn|secret" {
				t.Errorf("auth %v", f.auth)
			}
			m, err := mail.ReadMessage(strings.NewReader(f.got[0].data))
			if err != nil {
				t.Fatal(err)
			}
			if subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject")); subj != msg.Subject {
				t.Errorf("subject %q", subj)
			}
			if !strings.HasPrefix(m.Header.Get("Content-Type"), "multipart/mixed") || !strings.Contains(f.got[0].data, `filename="laptop.ovpn"`) {
				t.Errorf("no attachment: %s", f.got[0].data)
			}
			if !strings.Contains(f.got[0].data, base64.StdEncoding.EncodeToString(msg.Attachments[0].Data)[:40]) {
				t.Error("attachment content missing")
			}
		})
	}

	f := newFake(t, false)
	base := Config{Host: "127.0.0.1", Port: f.port(), Security: SecuritySTARTTLS, Username: "vpn", Password: "wrong", From: "vpn@example.com", InsecureSkipVerify: true}
	if err := Send(ctx, base, msg); err == nil || !strings.Contains(err.Error(), "login") {
		t.Errorf("wrong password: %v", err)
	}
	// The certificate is checked unless told otherwise.
	base.Password, base.InsecureSkipVerify = "secret", false
	if err := Send(ctx, base, msg); err == nil {
		t.Error("self-signed certificate accepted")
	}
	base.InsecureSkipVerify = true
	for name, m := range map[string]Message{
		"no recipient":   {Subject: "x", Text: "x"},
		"bad recipient":  {To: []string{"not an address"}, Subject: "x"},
		"header newline": {To: []string{"a@example.com"}, Subject: "x\r\nBcc: evil@example.com"},
		"bad attachment": {To: []string{"a@example.com"}, Subject: "x", Attachments: []Attachment{{Name: "a\"b", Data: []byte("x")}}},
	} {
		if err := Send(ctx, base, m); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
}

func TestValidate(t *testing.T) {
	ok := Config{Host: "smtp.example.com", Port: 587, Security: SecuritySTARTTLS, Username: "u", Password: "p", From: "trapdoor <vpn@example.com>"}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mod := range map[string]func(*Config){
		"host":     func(c *Config) { c.Host = "" },
		"port":     func(c *Config) { c.Port = 0 },
		"security": func(c *Config) { c.Security = "ssl" },
		"from":     func(c *Config) { c.From = "nobody" },
		"user":     func(c *Config) { c.Username = "" },
	} {
		c := ok
		mod(&c)
		if c.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
