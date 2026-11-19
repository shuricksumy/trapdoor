package web

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/mail"
)

// Email: an SMTP account (Account → Email), notifications to the admins,
// and clients' addresses to send them their profile.

const (
	settingEmail       = "email"
	settingEmailStatus = "email.status"
	settingExpirySent  = "email.expiry_sent"
	// nodeDownAfter is how long a node is unreachable before an email.
	nodeDownAfter = 5 * time.Minute
)

// Notify chooses the emails the admins get.
type Notify struct {
	BackupFailed bool `json:"backup_failed"`
	NodeDown     bool `json:"node_down"`
	CertExpiry   bool `json:"cert_expiry"`
}

// EmailSettings is GET / PUT /api/v1/email.
type EmailSettings struct {
	// Enabled sends emails; off keeps the settings.
	Enabled bool `json:"enabled"`
	mail.Config
	// Admins get the notifications.
	Admins []string `json:"admins"`
	Notify Notify   `json:"notify"`
	// PasswordSet (answer only): a password is stored; it is never sent back.
	PasswordSet bool `json:"password_set"`
	// ClearPassword (request only) removes the stored password; an empty
	// password otherwise keeps it.
	ClearPassword bool `json:"clear_password,omitempty"`
}

// EmailStatus is the outcome of the last email.
type EmailStatus struct {
	Time    time.Time `json:"time"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	Subject string    `json:"subject"`
}

func (s *Server) emailSettings(ctx context.Context) (EmailSettings, error) {
	es := EmailSettings{Config: mail.Config{Port: 587, Security: mail.SecuritySTARTTLS},
		Notify: Notify{BackupFailed: true, NodeDown: true, CertExpiry: true}}
	_, err := s.Stats.DB.Setting(ctx, settingEmail, &es)
	return es, err
}

func (es EmailSettings) on() bool { return es.Enabled && es.Host != "" }

func (s *Server) sendMail(ctx context.Context, c mail.Config, m mail.Message) error {
	send := s.SendMail
	if send == nil {
		send = mail.Send
	}
	err := send(ctx, c, m)
	st := EmailStatus{Time: s.Now().UTC(), OK: err == nil, Subject: m.Subject}
	if err != nil {
		st.Error = err.Error()
		s.Log.Warn("email not sent", "subject", m.Subject, "err", err)
	}
	_ = s.Stats.DB.SetSetting(context.WithoutCancel(ctx), settingEmailStatus, st)
	return err
}

// notify emails the admins when email is set up and the kind is chosen.
// It does not wait for the server and does not fail the caller.
func (s *Server) notify(ctx context.Context, want func(Notify) bool, subject, text string) {
	es, err := s.emailSettings(ctx)
	if err != nil || !es.on() || len(es.Admins) == 0 || !want(es.Notify) {
		return
	}
	s.mailWG.Add(1)
	go func() {
		defer s.mailWG.Done()
		_ = s.sendMail(context.WithoutCancel(ctx), es.Config, mail.Message{To: es.Admins, Subject: "trapdoor: " + subject,
			Text: text + "\n\n-- \ntrapdoor" + s.baseURLLine()})
	}()
}

func (s *Server) baseURLLine() string {
	if s.PublicURL == "" {
		return ""
	}
	return " · " + s.PublicURL
}

func (s *Server) registerEmailRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/email", s.handleGetEmail)
	mux.HandleFunc("PUT /api/v1/email", s.handleSetEmail)
	mux.HandleFunc("POST /api/v1/email/test", s.handleTestEmail)
	mux.HandleFunc("GET /api/v1/servers/{server}/emails", s.handleClientEmails)
	mux.HandleFunc("PUT /api/v1/servers/{server}/emails/{name}", s.handleSetClientEmail)
	mux.HandleFunc("POST /api/v1/servers/{server}/emails/{name}/send", s.handleSendProfile)
}

func (s *Server) handleGetEmail(w http.ResponseWriter, r *http.Request) {
	es, err := s.emailSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	es.PasswordSet, es.Password = es.Password != "", ""
	var st EmailStatus
	out := struct {
		EmailSettings
		Last *EmailStatus `json:"last,omitempty"`
	}{EmailSettings: es}
	if ok, _ := s.Stats.DB.Setting(r.Context(), settingEmailStatus, &st); ok {
		out.Last = &st
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetEmail(w http.ResponseWriter, r *http.Request) {
	var req EmailSettings
	if !decodeJSON(w, r, &req) {
		return
	}
	old, err := s.emailSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Host = strings.TrimSpace(req.Host)
	req.Username = strings.TrimSpace(req.Username)
	req.From = strings.TrimSpace(req.From)
	switch {
	case req.ClearPassword:
		req.Password = ""
	case req.Password == "":
		req.Password = old.Password
	}
	if req.Enabled || req.Host != "" {
		if err := req.Validate(); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Admins, err = mail.ParseAddresses(req.Admins); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "admin emails: "+err.Error())
		return
	}
	req.PasswordSet, req.ClearPassword = false, false
	if err := s.Stats.DB.SetSetting(r.Context(), settingEmail, req); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "email.settings", req.Host)
	req.PasswordSet, req.Password = req.Password != "", ""
	httpx.WriteJSON(w, http.StatusOK, req)
}

func (s *Server) handleTestEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		To string `json:"to"`
	}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	es, err := s.emailSettings(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !es.on() {
		httpx.WriteError(w, http.StatusBadRequest, "set up and turn on email first")
		return
	}
	to := es.Admins
	if req.To != "" {
		to = []string{req.To}
	}
	if len(to) == 0 {
		httpx.WriteError(w, http.StatusBadRequest, "no admin email to send it to")
		return
	}
	if err := s.sendMail(r.Context(), es.Config, mail.Message{To: to, Subject: "trapdoor: test email",
		Text: "This is a test from trapdoor. Email works." + "\n\n-- \ntrapdoor" + s.baseURLLine()}); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "not sent: "+err.Error())
		return
	}
	s.audit(r, "email.test", strings.Join(to, ","))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sent_to": to})
}

func (s *Server) handleClientEmails(w http.ResponseWriter, r *http.Request) {
	m, err := s.Stats.DB.ClientEmails(r.Context(), r.PathValue("server"))
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, m)
}

func oneAddress(a string) (string, error) {
	if strings.TrimSpace(a) == "" {
		return "", nil
	}
	l, err := mail.ParseAddresses([]string{a})
	if err != nil || len(l) != 1 {
		return "", fmt.Errorf("not an email address: %q", a)
	}
	return l[0], nil
}

func (s *Server) handleSetClientEmail(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email string `json:"email"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	addr, err := oneAddress(req.Email)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.Stats.DB.SetClientEmail(r.Context(), r.PathValue("server"), r.PathValue("name"), addr); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"email": addr})
}

// SendProfileRequest is POST …/emails/{name}/send.
type SendProfileRequest struct {
	// Email to send to (also stored); empty = the stored one.
	Email string `json:"email"`
	// QRGIF is a QR code of a WireGuard config made by the UI (base64), attached.
	QRGIF string `json:"qr_gif,omitempty"`
	// Note is added to the text.
	Note string `json:"note,omitempty"`
}

var appLinks = map[string]string{
	api.TypeOpenVPN:   "OpenVPN Connect: https://openvpn.net/client/ (Windows, macOS, iPhone, Android), or Tunnelblick on macOS",
	api.TypeWireGuard: "WireGuard: https://www.wireguard.com/install/ (Windows, macOS, iPhone, Android, Linux)",
	api.TypeAmneziaWG: "AmneziaWG (or AmneziaVPN): https://amnezia.org/en/downloads (Windows, macOS, iPhone, Android)",
}

func (s *Server) handleSendProfile(w http.ResponseWriter, r *http.Request) {
	var req SendProfileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	id, name := r.PathValue("server"), r.PathValue("name")
	es, err := s.emailSettings(ctx)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !es.on() {
		httpx.WriteError(w, http.StatusBadRequest, "set up email first (Account → Email)")
		return
	}
	to, err := oneAddress(req.Email)
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if to == "" {
		emails, _ := s.Stats.DB.ClientEmails(ctx, id)
		to = emails[name]
	}
	if to == "" {
		httpx.WriteError(w, http.StatusBadRequest, "no email address for "+name)
		return
	}
	ag := s.Agent.Server(id)
	info, err := ag.ServerInfo(ctx)
	if err != nil {
		s.agentError(w, err)
		return
	}
	msg := mail.Message{To: []string{to}}
	var steps []string
	switch info.Type {
	case api.TypeOpenVPN:
		profile, err := ag.Profile(ctx, name)
		if err != nil {
			s.agentError(w, err)
			return
		}
		msg.Attachments = append(msg.Attachments, mail.Attachment{Name: name + ".ovpn", ContentType: "application/x-openvpn-profile", Data: profile})
		steps = []string{"Install " + appLinks[info.Type] + ".", "Open the attached " + name + ".ovpn with it (or import it in the app)."}
	default:
		conf, err := ag.PeerConf(ctx, name)
		if err != nil {
			s.agentError(w, err)
			return
		}
		msg.Attachments = append(msg.Attachments, mail.Attachment{Name: name + ".conf", ContentType: "text/plain", Data: conf})
		steps = []string{"Install " + appLinks[info.Type] + ".", "In the app, add a tunnel from the attached " + name + ".conf (Import from file)."}
		if req.QRGIF != "" {
			steps[1] = "On a phone: add a tunnel by scanning the attached QR code shown on another screen, or import " + name + ".conf. On a computer: import " + name + ".conf (Import tunnel from file)."
			qr, err := base64.StdEncoding.DecodeString(req.QRGIF)
			if err != nil || len(qr) > 512<<10 {
				httpx.WriteError(w, http.StatusBadRequest, "qr_gif: base64 of a GIF")
				return
			}
			msg.Attachments = append(msg.Attachments, mail.Attachment{Name: name + "-qr.gif", ContentType: "image/gif", Data: qr})
		}
	}
	server := info.Name
	if server == "" {
		server = info.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hello,\n\nhere is your VPN access \"%s\" for %s (%s).\n\n", name, server, typeName(info.Type))
	for i, st := range steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, st)
	}
	fmt.Fprintf(&b, "%d. Connect.\n", len(steps)+1)
	if n := strings.TrimSpace(req.Note); n != "" {
		b.WriteString("\n" + n + "\n")
	}
	b.WriteString("\nThe attached file is your personal key: keep it private, do not forward it, and delete this email once it works.\n")
	msg.Subject = "Your VPN access: " + server
	msg.Text = b.String()
	if err := s.sendMail(ctx, es.Config, msg); err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "not sent: "+err.Error())
		return
	}
	if req.Email != "" {
		_ = s.Stats.DB.SetClientEmail(ctx, id, name, to)
	}
	s.audit(r, "client.send", name+" → "+to)
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"sent_to": to})
}

func typeName(t string) string {
	switch t {
	case api.TypeWireGuard:
		return "WireGuard"
	case api.TypeAmneziaWG:
		return "AmneziaWG"
	default:
		return "OpenVPN"
	}
}

// nodeWatch remembers since when each node is unreachable.
type nodeWatch struct {
	mu   sync.Mutex
	down map[string]nodeDown
}

type nodeDown struct {
	since    time.Time
	notified bool
	err      string
}

// WatchNodes emails the admins when a node is unreachable for a few minutes,
// and again when it answers.
func (s *Server) WatchNodes(ctx context.Context) {
	if s.Hosts == nil {
		return
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkNodes(ctx)
		}
	}
}

func (s *Server) checkNodes(ctx context.Context) {
	hosts := s.Hosts.Hosts(ctx)
	now := s.Now()
	s.nodes.mu.Lock()
	defer s.nodes.mu.Unlock()
	if s.nodes.down == nil {
		s.nodes.down = map[string]nodeDown{}
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		seen[h.Name] = true
		d, wasDown := s.nodes.down[h.Name]
		switch {
		case h.Error != "" && !wasDown:
			s.nodes.down[h.Name] = nodeDown{since: now, err: h.Error}
		case h.Error != "" && !d.notified && now.Sub(d.since) >= nodeDownAfter:
			d.notified = true
			s.nodes.down[h.Name] = d
			s.notify(ctx, func(n Notify) bool { return n.NodeDown }, "node "+h.Name+" is not reachable",
				fmt.Sprintf("Node %s (%s) has not answered since %s:\n%s\n\nIts VPN servers may still be running; the web UI cannot manage them until it answers.",
					h.Name, h.URL, d.since.In(s.Location).Format("2006-01-02 15:04"), h.Error))
		case h.Error == "" && wasDown:
			delete(s.nodes.down, h.Name)
			if d.notified {
				s.notify(ctx, func(n Notify) bool { return n.NodeDown }, "node "+h.Name+" is back",
					fmt.Sprintf("Node %s (%s) answers again (unreachable for %s).", h.Name, h.URL, now.Sub(d.since).Round(time.Minute)))
			}
		}
	}
	for n := range s.nodes.down {
		if !seen[n] {
			delete(s.nodes.down, n) // removed meanwhile
		}
	}
}

// expiringCert is a certificate that needs renewing soon.
type expiringCert struct {
	server, name string
	notAfter     time.Time
}

// notifyExpiry emails the certificates that newly entered the warning
// window (each one once).
func (s *Server) notifyExpiry(ctx context.Context, certs []expiringCert) {
	sent := map[string]bool{}
	_, _ = s.Stats.DB.Setting(ctx, settingExpirySent, &sent)
	keep := map[string]bool{}
	var fresh []expiringCert
	for _, c := range certs {
		k := c.server + "|" + c.name + "|" + c.notAfter.UTC().Format(time.RFC3339)
		keep[k] = true
		if !sent[k] {
			fresh = append(fresh, c)
		}
	}
	if len(keep) != len(sent) || len(fresh) > 0 {
		_ = s.Stats.DB.SetSetting(ctx, settingExpirySent, keep)
	}
	if len(fresh) == 0 {
		return
	}
	sort.Slice(fresh, func(i, j int) bool { return fresh[i].notAfter.Before(fresh[j].notAfter) })
	var b strings.Builder
	b.WriteString("These certificates expire soon; their clients cannot connect afterwards. Renew them on the Clients page (also in bulk).\n\n")
	for _, c := range fresh {
		days := int(c.notAfter.Sub(s.Now()).Hours() / 24)
		fmt.Fprintf(&b, "- %s on %s: %s (in %d days)\n", c.name, c.server, c.notAfter.In(s.Location).Format("2006-01-02"), days)
	}
	subject := fmt.Sprintf("%d certificates expire soon", len(fresh))
	if len(fresh) == 1 {
		subject = "a certificate expires soon"
	}
	s.notify(ctx, func(n Notify) bool { return n.CertExpiry }, subject, b.String())
}
