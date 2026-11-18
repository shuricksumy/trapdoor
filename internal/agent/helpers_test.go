package agent

import "net/http"

// serve returns the agent API with s as its only server.
func serve(s *Server) http.Handler { return NewHost(s.cfg.Token, s.log, s).Handler() }
