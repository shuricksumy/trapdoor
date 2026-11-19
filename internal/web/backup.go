package web

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/shuricksumy/trapdoor/internal/backup"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/version"
)

// BackupSource gives the agents' parts of a backup.
type BackupSource interface {
	// BackupHosts are the other hosts to back up as well.
	BackupHosts() []string
	// AgentBackup streams the own agent's (host "") or a host's tar.
	AgentBackup(ctx context.Context, host string) (io.ReadCloser, error)
}

// BackupHosts are the other hosts' names.
func (m *MultiAgent) BackupHosts() []string { return m.hostList() }

// AgentBackup streams an agent's directories.
func (m *MultiAgent) AgentBackup(ctx context.Context, host string) (io.ReadCloser, error) {
	if host == "" {
		return m.local.Backup(ctx)
	}
	r, ok := m.host(host)
	if !ok {
		return nil, os.ErrNotExist
	}
	return r.c.Backup(ctx)
}

// BackupRequest is the body of POST /api/v1/backup.
type BackupRequest struct {
	// Passphrase encrypts the file (recommended: it holds the CA keys and
	// the hosts' tokens); empty = a plain .tar.gz.
	Passphrase string `json:"passphrase"`
}

func (s *Server) registerBackupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/backup", s.handleBackup)
}

// handleBackup streams one file: manifest.json, the own agent's
// directories (agent/…), the web database (web/trapdoor.db) and every other
// host's directories (hosts/<name>/…).
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "backups are not available")
		return
	}
	var req BackupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	// Start everything that can fail before the first byte is sent.
	snap, err := s.Stats.DB.Snapshot(ctx)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "database snapshot: "+err.Error())
		return
	}
	defer func() { _ = os.Remove(snap) }()
	own, err := s.Backup.AgentBackup(ctx, "")
	if err != nil {
		s.agentError(w, err)
		return
	}
	defer func() { _ = own.Close() }()

	hosts := s.Backup.BackupHosts()
	m := backup.Manifest{Format: 1, Version: version.Version, Created: s.Now().UTC(), Parts: []string{"agent", "web"}}
	if info, err := s.Agent.Info(ctx); err == nil {
		m.Hostname = info.Hostname
	}
	for _, h := range hosts {
		m.Parts = append(m.Parts, "hosts/"+h)
	}

	name := "trapdoor-backup-" + s.Now().In(s.Location).Format("20060102-1504")
	if req.Passphrase != "" {
		name += ".tdbackup"
	} else {
		name += ".tar.gz"
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")

	var out io.Writer = w
	var enc io.WriteCloser
	if req.Passphrase != "" {
		if enc, err = backup.NewEncrypter(w, req.Passphrase); err != nil {
			s.Log.Error("backup", "err", err)
			return
		}
		out = enc
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	fail := func(what string, err error) {
		// Headers are sent: stop here, so the file is unreadable rather
		// than silently incomplete.
		s.Log.Error("backup failed", "part", what, "err", err)
	}
	mj, _ := json.MarshalIndent(m, "", "  ")
	if err := writeEntry(tw, "manifest.json", mj, m.Created); err != nil {
		fail("manifest", err)
		return
	}
	if err := backup.Copy(tw, own, "agent"); err != nil {
		fail("agent", err)
		return
	}
	if err := addFile(tw, snap, "web/trapdoor.db"); err != nil {
		fail("web", err)
		return
	}
	for _, h := range hosts {
		hctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		rc, err := s.Backup.AgentBackup(hctx, h)
		if err == nil {
			err = backup.Copy(tw, rc, "hosts/"+h)
			_ = rc.Close()
		}
		cancel()
		if err != nil {
			// One unreachable host should not cost the rest of the backup.
			s.Log.Warn("backup: host left out", "host", h, "err", err)
			if err := writeEntry(tw, "hosts/"+h+".error", []byte(err.Error()+"\n"), m.Created); err != nil {
				fail(h, err)
				return
			}
		}
	}
	if err := tw.Close(); err != nil {
		fail("tar", err)
		return
	}
	if err := gz.Close(); err != nil {
		fail("gzip", err)
		return
	}
	if enc != nil {
		if err := enc.Close(); err != nil {
			fail("encryption", err)
			return
		}
	}
	s.audit(r, "backup.download", name)
}

func writeEntry(tw *tar.Writer, name string, data []byte, t time.Time) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: t, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

func addFile(tw *tar.Writer, path, name string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: fi.Size(), ModTime: fi.ModTime(), Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err = io.CopyN(tw, f, fi.Size())
	return err
}
