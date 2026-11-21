package agent

import (
	"context"
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/config"
)

func TestTools(t *testing.T) {
	c, _ := firewallClient(t, config.Agent{Firewall: "off"})
	ctx := context.Background()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)

	res, err := c.RunTool(ctx, "tcp", api.ToolRequest{Target: "127.0.0.1", Ports: port})
	if err != nil || !res.OK || res.Tool != "tcp" || res.Summary != "1 of 1 port open" {
		t.Fatalf("tcp: %+v %v", res, err)
	}
	// refused before anything runs
	if _, err := c.RunTool(ctx, "ping", api.ToolRequest{Target: "-f 10.8.0.1"}); err == nil || !strings.Contains(err.Error(), "400") {
		t.Errorf("bad target: %v", err)
	}
	if _, err := c.RunTool(ctx, "nmap", api.ToolRequest{Target: "10.8.0.1"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("unknown tool: %v", err)
	}
	// one at a time per node
	toolsBusy <- struct{}{}
	_, err = c.RunTool(ctx, "tcp", api.ToolRequest{Target: "127.0.0.1", Ports: port})
	<-toolsBusy
	if err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("busy: %v", err)
	}
}
