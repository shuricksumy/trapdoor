// Command trapdoor runs the trapdoor agent or web process.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TD_TZ works in minimal containers without zoneinfo

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/auth"
	"github.com/shuricksumy/trapdoor/internal/clientauth"
	"github.com/shuricksumy/trapdoor/internal/config"
	"github.com/shuricksumy/trapdoor/internal/history"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/pki"
	"github.com/shuricksumy/trapdoor/internal/store"
	"github.com/shuricksumy/trapdoor/internal/version"
	"github.com/shuricksumy/trapdoor/internal/web"
)

const usage = `usage: trapdoor <command>

commands:
  agent     run the privileged agent (PKI, OpenVPN control)
  web       run the web UI and API
  init-pki  create a new CA in TD_PKI_DIR (CA name from TD_CA_NAME)
  user add|passwd|del|list [name]
            manage admin users in TD_DB_PATH (password from TD_ADMIN_PASSWORD or stdin)
  auth-verify [--ccd DIR] <file>
            OpenVPN auth-user-pass-verify (via-file) for client 2FA
  restore agent|web [--host NAME] [--force] <file>
            restore a backup made in the web UI (trapdoor stopped)
  agent-cert
            print the agent's TLS fingerprint (TD_AGENT_TLS=auto), creating the certificate
  health    <url>  exit 0 if GET <url>/healthz answers 200 (http://, https:// or unix:/path)
  version   print version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "agent":
		err = runAgent(ctx, log)
	case "web":
		err = runWeb(ctx, log)
	case "init-pki":
		err = runInitPKI(log)
	case "user":
		err = runUser(ctx, os.Args[2:])
	case "version":
		fmt.Println(version.String())
	case "health":
		err = runHealth(ctx, os.Args[2:])
	case "agent-cert":
		err = runAgentCert()
	case "restore":
		err = runRestore(os.Args[2:])
	case "auth-verify":
		os.Exit(runAuthVerify(os.Args[2:]))
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func runAgent(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil {
		return err
	}
	l, err := httpx.Listen(cfg.Listen)
	if err != nil {
		return err
	}
	if cfg.TLS == "auto" {
		cert, fp, err := agent.AgentCert(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return fmt.Errorf("agent TLS certificate: %w", err)
		}
		l = tls.NewListener(l, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
		log.Info("agent API over TLS: add this host in the web UI with its fingerprint", "fingerprint", fp)
	}
	if path, ok := strings.CutPrefix(cfg.Listen, "unix:"); ok && cfg.SocketGID >= 0 {
		if err := os.Chown(path, -1, cfg.SocketGID); err != nil {
			return fmt.Errorf("chown agent socket: %w", err)
		}
	}
	log = log.With("proc", "agent")

	log.Info("listening", "addr", cfg.Listen, "version", version.Version)
	host := agent.NewHost(cfg.Token, log)
	if cfg.OpenVPN == "off" {
		log.Info("OpenVPN is off on this agent (TD_OPENVPN=off)")
	} else if err := addOpenVPN(ctx, cfg, log, host); err != nil {
		return err
	}
	if cfg.WireGuard != "off" {
		wgs, err := agent.LoadWireGuard(ctx, cfg, log)
		if err != nil {
			return fmt.Errorf("WireGuard servers: %w", err)
		}
		host.EnableWireGuard(cfg, wgs)
		log.Info("WireGuard", "mode", cfg.WireGuard, "dir", cfg.WireGuardDir, "servers", len(wgs))
	}
	host.SetNames(cfg.Machine, cfg.NodeName)
	go host.WatchClientExpiry(ctx)
	if cfg.JoinURL != "" {
		_, fp, err := agent.AgentCert(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return err
		}
		go func() {
			_ = agent.Join(ctx, log, cfg.JoinURL, api.JoinRequest{Token: cfg.JoinToken, Name: cfg.NodeName,
				URL: cfg.NodeURL, Fingerprint: fp, AgentToken: cfg.Token})
		}()
	}
	return httpx.Serve(ctx, l, host.Handler())
}

// addOpenVPN mounts the first OpenVPN server (TD_* settings) and the ones
// added in trapdoor.
func addOpenVPN(ctx context.Context, cfg config.Agent, log *slog.Logger, host *agent.Host) error {
	var p pki.PKI
	store, err := pki.Open(cfg.PKIDir, pki.Options{CAPassphrase: cfg.CAPassphrase, CRLCopies: cfg.CRLCopies})
	switch {
	case err == nil:
		p = store
		log.Info("PKI opened", "dir", cfg.PKIDir)
	case errors.Is(err, pki.ErrNotInitialized):
		log.Warn("no PKI yet; certificate API disabled until setup", "dir", cfg.PKIDir)
	default:
		return err
	}
	srv := agent.New(cfg, log, p)
	go srv.ApplyFirewallAtStart(ctx)
	extra, err := agent.LoadOpenVPN(cfg, log)
	if err != nil {
		return fmt.Errorf("OpenVPN servers: %w", err)
	}
	for _, s := range extra {
		go s.ApplyFirewallAtStart(ctx)
	}
	host.EnableOpenVPN(cfg, append([]*agent.Server{srv}, extra...))
	if len(extra) > 0 {
		log.Info("OpenVPN servers", "dir", cfg.OpenVPNServersDir, "added", len(extra))
	}
	return nil
}

func runWeb(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.LoadWeb()
	if err != nil {
		return err
	}
	l, err := httpx.Listen(cfg.Listen)
	if err != nil {
		return err
	}
	log = log.With("proc", "web")
	client := agent.NewClient(cfg.AgentURL, cfg.AgentToken, cfg.AgentTimeout)

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// The own agent and the other hosts added in the UI.
	agents, err := web.NewMultiAgent(ctx, client, db, cfg.AgentTimeout)
	if err != nil {
		return err
	}
	poller := &history.Poller{DB: db, Interval: cfg.PollInterval, Log: log,
		List: func(ctx context.Context) ([]string, error) {
			info, err := agents.Info(ctx)
			ids := make([]string, 0, len(info.Servers))
			for _, s := range info.Servers {
				ids = append(ids, s.ID)
			}
			return ids, err
		},
		Source: func(id string) history.Source { return agents.Server(id) },
	}
	go poller.Run(ctx)

	authSvc := auth.New(db, auth.Options{IdleTimeout: cfg.SessionIdle})
	if n, err := authSvc.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("no admin user yet: create one with 'trapdoor user add <name>'")
	}
	if !cfg.CookieSecure {
		log.Warn("TD_COOKIE_SECURE=false: login cookie is sent over plain HTTP")
	}
	srv := web.New(web.Deps{
		Agent: agents, Hosts: agents, Backup: agents, JoinSecret: cfg.JoinSecret, Stats: &history.Stats{DB: db},
		Auth: authSvc, APIToken: cfg.APIToken, CookieSecure: cfg.CookieSecure,
		TrustedProxies: cfg.TrustedProxies, Location: cfg.Location, Log: log,
		ExpiryWarnDays: cfg.ExpiryWarnDays, BackupDir: cfg.BackupDir, BackupPassphrase: cfg.BackupPassphrase,
		PublicURL: cfg.PublicURL, FrameAncestors: cfg.FrameAncestors,
	})
	go srv.WatchExpiry(ctx)
	go srv.RunBackups(ctx)
	go srv.WatchNodes(ctx)
	go srv.WatchPresence(ctx)
	log.Info("listening", "addr", cfg.Listen, "agent", cfg.AgentURL, "db", cfg.DBPath,
		"tz", cfg.Location.String(), "version", version.Version)
	return httpx.Serve(ctx, l, srv.Handler())
}

func runInitPKI(log *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil && !errors.Is(err, config.ErrNoToken) {
		return err
	}
	name := os.Getenv("TD_CA_NAME")
	if _, err := pki.Init(cfg.PKIDir, pki.InitOptions{CommonName: name},
		pki.Options{CAPassphrase: cfg.CAPassphrase, CRLCopies: cfg.CRLCopies}); err != nil {
		return err
	}
	log.Info("PKI created", "dir", cfg.PKIDir)
	return nil
}

func runUser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: trapdoor user add|passwd|del|list [name]")
	}
	cfg, err := config.LoadWeb()
	if err != nil && !errors.Is(err, config.ErrNoToken) {
		return err
	}
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	svc := auth.New(db, auth.Options{})

	name := ""
	if len(args) > 1 {
		name = args[1]
	}
	need := func() error {
		if name == "" {
			return fmt.Errorf("usage: trapdoor user %s <name>", args[0])
		}
		return nil
	}
	switch args[0] {
	case "list":
		users, err := svc.Users(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			fmt.Printf("%s\tcreated %s\n", u.Username, u.CreatedAt.Format(time.DateOnly))
		}
		return nil
	case "add":
		if err := need(); err != nil {
			return err
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if _, err := svc.CreateUser(ctx, name, pw); err != nil {
			return err
		}
		fmt.Printf("user %s created\n", name)
		return nil
	case "passwd":
		if err := need(); err != nil {
			return err
		}
		pw, err := readPassword()
		if err != nil {
			return err
		}
		if err := svc.SetPassword(ctx, name, pw); err != nil {
			return err
		}
		fmt.Printf("password of %s changed; their sessions were logged out\n", name)
		return nil
	case "del":
		if err := need(); err != nil {
			return err
		}
		if err := svc.DeleteUser(ctx, name); err != nil {
			return err
		}
		fmt.Printf("user %s deleted\n", name)
		return nil
	default:
		return fmt.Errorf("unknown user command %q", args[0])
	}
}

// readPassword takes TD_ADMIN_PASSWORD, else the first line of stdin, so it
// works in scripts and with "docker exec -i ... trapdoor user add admin".
func readPassword() (string, error) {
	if pw := os.Getenv("TD_ADMIN_PASSWORD"); pw != "" {
		return pw, nil
	}
	fmt.Fprint(os.Stderr, "password: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// runHealth is the container health check (no curl in the image needed).
func runAgentCert() error {
	dir := env("TD_OPENVPN_DIR", "/etc/openvpn")
	_, fp, err := agent.AgentCert(env("TD_AGENT_TLS_CERT", dir+"/trapdoor-agent.crt"), env("TD_AGENT_TLS_KEY", dir+"/trapdoor-agent.key"))
	if err != nil {
		return err
	}
	fmt.Println(fp)
	return nil
}

func runHealth(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: trapdoor health http://127.0.0.1:8080 | unix:/path/to/agent.sock")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	c := agent.NewClient(args[0], "", 5*time.Second)
	if strings.HasPrefix(args[0], "https://") {
		// Health only (no token): the agent's self-signed certificate is not checked.
		c = agent.NewInsecureHealthClient(args[0], 5*time.Second)
	}
	return c.Health(ctx)
}

// runAuthVerify is called by OpenVPN for every login:
//
//	script-security 2
//	auth-user-pass-verify "/usr/local/bin/trapdoor auth-verify" via-file
//
// Exit 0 allows the client, anything else denies it. Messages go to stderr,
// which ends up in OpenVPN's log; passwords are never printed.
func runAuthVerify(args []string) int {
	ccd := env("TD_CCD_DIR", env("TD_OPENVPN_DIR", "/etc/openvpn")+"/ccd")
	var file string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--ccd" && i+1 < len(args):
			ccd = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--ccd="):
			ccd = strings.TrimPrefix(args[i], "--ccd=")
		default:
			file = args[i]
		}
	}
	if file == "" {
		fmt.Fprintln(os.Stderr, "auth-verify: usage: trapdoor auth-verify [--ccd DIR] <credentials file>")
		return 2
	}
	user, err := clientauth.VerifyFile(ccd, file, os.Getenv("common_name"), time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "auth-verify: DENIED %q: %v\n", user, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "auth-verify: OK %q\n", user)
	return 0
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
