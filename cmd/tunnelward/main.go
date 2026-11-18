// Command tunnelward runs the tunnelward agent or web process.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/shuricksumy/tunnelward/internal/agent"
	"github.com/shuricksumy/tunnelward/internal/config"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/version"
	"github.com/shuricksumy/tunnelward/internal/web"
)

const usage = `usage: tunnelward <command>

commands:
  agent     run the privileged agent (PKI, OpenVPN control)
  web       run the web UI and API
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
	log.Info("listening", "addr", cfg.Listen, "version", version.Version)
	return httpx.Serve(ctx, l, agent.New(cfg, log).Handler())
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
	log.Info("listening", "addr", cfg.Listen, "agent", cfg.AgentURL, "version", version.Version)
	return httpx.Serve(ctx, l, web.New(client, log).Handler())
}
