package web

import (
	"net/http"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// handleTool runs a network tool in the server's network (through its
// agent) and writes an audit line with what was asked.
func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	var req api.ToolRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	tool := r.PathValue("tool")
	target := req.Target
	if tool == "http" {
		target = req.URL
	}
	s.audit(r, "tool."+tool, target)
	res, err := s.agent(r).RunTool(r.Context(), tool, req)
	if err != nil {
		s.agentError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}
