// Command tunnelward runs the tunnelward agent or web process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata" // TW_TZ works in minimal containers without zoneinfo

	"github.com/shuricksumy/tunnelward/internal/agent"
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
	case "version":
		fmt.Println(version.String())
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

	if cfg.APIToken == "" {
		log.Warn("TW_WEB_API_TOKEN not set: /api/v1 is disabled")
	}
	srv := web.New(web.Deps{
		Agent: client, Stats: &history.Stats{DB: db, Agent: agentID},
		APIToken: cfg.APIToken, Location: cfg.Location, Log: log,
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
