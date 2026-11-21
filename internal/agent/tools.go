package agent

import (
	"context"
	"errors"
	"net/http"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/nettools"
)

// toolsBusy lets one tool run at a time on this agent (they share the
// node's network and some take seconds).
var toolsBusy = make(chan struct{}, 1)

// registerTools adds POST /tools/{tool}: network diagnostics run in this
// agent's network, which in Docker is the vpn container's (the server's).
func registerTools(mux *http.ServeMux) {
	mux.HandleFunc("POST /tools/{tool}", handleTool)
}

func handleTool(w http.ResponseWriter, r *http.Request) {
	var req api.ToolRequest
	if r.ContentLength != 0 && !decodeBody(w, r, &req) {
		return
	}
	select {
	case toolsBusy <- struct{}{}:
		defer func() { <-toolsBusy }()
	default:
		httpx.WriteError(w, http.StatusConflict, "another tool is running on this node; try again in a moment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nettools.MaxTimeout)
	defer cancel()
	tool := r.PathValue("tool")
	var res nettools.Result
	var err error
	switch tool {
	case "ping":
		res, err = nettools.Ping(ctx, req.Target, req.Count)
	case "traceroute":
		res, err = nettools.Traceroute(ctx, req.Target)
	case "route":
		res, err = nettools.Route(ctx, req.Target)
	case "dns":
		res, err = nettools.DNS(ctx, req.Target, req.Kind, req.Server)
	case "tcp":
		res, err = nettools.TCP(ctx, req.Target, req.Ports)
	case "http":
		res, err = nettools.HTTP(ctx, req.URL)
	default:
		httpx.WriteError(w, http.StatusNotFound, "no such tool: ping, traceroute, route, dns, tcp or http")
		return
	}
	if errors.Is(err, nettools.ErrBadInput) {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, api.ToolResult{Tool: tool, OK: res.OK, Summary: res.Summary, Output: res.Output, Truncated: res.Truncated, Millis: res.Millis})
}
