// Package httpx holds small HTTP helpers shared by the agent and web servers.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
)

// Listen opens a listener for "host:port" or "unix:/path".
// An existing unix socket file is removed first.
func Listen(addr string) (net.Listener, error) {
	if path, ok := strings.CutPrefix(addr, "unix:"); ok {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		l, err := net.Listen("unix", path)
		if err != nil {
			return nil, err
		}
		// Only the owner and group (e.g. a shared docker volume) may connect.
		if err := os.Chmod(path, 0o660); err != nil {
			l.Close()
			return nil, err
		}
		return l, nil
	}
	return net.Listen("tcp", addr)
}

// Serve runs h on l until ctx is cancelled, then shuts down gracefully.
func Serve(ctx context.Context, l net.Listener, h http.Handler) error {
	srv := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(l) }()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

// WriteJSON writes v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err)
	}
}

// WriteError writes an api.Error with the given status code.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, api.Error{Error: msg})
}

// LogRequests logs method, path, status and duration of every request.
func LogRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"dur", time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}
