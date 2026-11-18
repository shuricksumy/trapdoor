// Command tunnelward runs the tunnelward agent or web process.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TW_TZ works in minimal containers without zoneinfo

	"github.com/shuricksumy/tunnelward/internal/agent"
	"github.com/shuricksumy/tunnelward/internal/auth"
	"github.com/shuricksumy/tunnelward/internal/config"
	"github.com/shuricksumy/tunnelward/internal/history"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/pki"
	"github.com/shuricksumy/tunnelward/internal/store"
	"github.com/shuricksumy/tunnelward/internal/version"
	"github.com/shuricksumy/tunnelward/internal/web"
)

const usage = `usage: tunnelward <command>

commands:
  agent     run the privileged agent (PKI, OpenVPN control)
  web       run the web UI and API
  init-pki  create a new CA in TW_PKI_DIR (CA name from TW_CA_NAME)
  user add|passwd|del|list [name]
            manage admin users in TW_DB_PATH (password from TW_ADMIN_PASSWORD or stdin)
  health    <url>  exit 0 if GET <url>/healthz answers 200 (http://... or unix:/path)
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
	if path, ok := strings.CutPrefix(cfg.Listen, "unix:"); ok && cfg.SocketGID >= 0 {
		if err := os.Chown(path, -1, cfg.SocketGID); err != nil {
			return fmt.Errorf("chown agent socket: %w", err)
		}
	}
	log = log.With("proc", "agent")

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

	log.Info("listening", "addr", cfg.Listen, "version", version.Version)
	return httpx.Serve(ctx, l, agent.New(cfg, log, p).Handler())
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

	const agentID = "default" // multi-server later
	col := &history.Collector{DB: db, Source: client, Agent: agentID, Interval: cfg.PollInterval, Log: log}
	go col.Run(ctx)

	authSvc := auth.New(db, auth.Options{IdleTimeout: cfg.SessionIdle})
	if n, err := authSvc.CountUsers(ctx); err == nil && n == 0 {
		log.Warn("no admin user yet: create one with 'tunnelward user add <name>'")
	}
	if !cfg.CookieSecure {
		log.Warn("TW_COOKIE_SECURE=false: login cookie is sent over plain HTTP")
	}
	srv := web.New(web.Deps{
		Agent: client, Stats: &history.Stats{DB: db, Agent: agentID},
		Auth: authSvc, APIToken: cfg.APIToken, CookieSecure: cfg.CookieSecure,
		TrustedProxies: cfg.TrustedProxies, Location: cfg.Location, Log: log,
	})
	log.Info("listening", "addr", cfg.Listen, "agent", cfg.AgentURL, "db", cfg.DBPath,
		"tz", cfg.Location.String(), "version", version.Version)
	return httpx.Serve(ctx, l, srv.Handler())
}

func runInitPKI(log *slog.Logger) error {
	cfg, err := config.LoadAgent()
	if err != nil && !errors.Is(err, config.ErrNoToken) {
		return err
	}
	name := os.Getenv("TW_CA_NAME")
	if _, err := pki.Init(cfg.PKIDir, pki.InitOptions{CommonName: name},
		pki.Options{CAPassphrase: cfg.CAPassphrase, CRLCopies: cfg.CRLCopies}); err != nil {
		return err
	}
	log.Info("PKI created", "dir", cfg.PKIDir)
	return nil
}

func runUser(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tunnelward user add|passwd|del|list [name]")
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
			return fmt.Errorf("usage: tunnelward user %s <name>", args[0])
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

// readPassword takes TW_ADMIN_PASSWORD, else the first line of stdin, so it
// works in scripts and with "docker exec -i ... tunnelward user add admin".
func readPassword() (string, error) {
	if pw := os.Getenv("TW_ADMIN_PASSWORD"); pw != "" {
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
func runHealth(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tunnelward health http://127.0.0.1:8080 | unix:/path/to/agent.sock")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return agent.NewClient(args[0], "", 5*time.Second).Health(ctx)
}
