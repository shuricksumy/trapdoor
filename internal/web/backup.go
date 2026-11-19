package web

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/api"
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
	// RestoreServer brings a server back on host ("" = this machine) from
	// the tar of its backup; the id in the answer is the web API's.
	RestoreServer(ctx context.Context, host string, tarball io.Reader, replace bool) (api.ServerInfo, error)
}

// RestoreServer restores a server on a machine.
func (m *MultiAgent) RestoreServer(ctx context.Context, host string, tarball io.Reader, replace bool) (api.ServerInfo, error) {
	if host == "" {
		return m.local.RestoreServer(ctx, tarball, replace)
	}
	r, ok := m.host(host)
	if !ok {
		return api.ServerInfo{}, &agent.APIError{Status: http.StatusNotFound, Message: "no host " + host}
	}
	info, err := r.c.RestoreServer(ctx, tarball, replace)
	if err == nil {
		info.ID, info.Host = host+"."+info.ID, host
	}
	return info, err
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
	mux.HandleFunc("POST /api/v1/servers/{server}/backup", s.handleServerBackup)
	mux.HandleFunc("POST /api/v1/servers/restore", s.handleRestoreServer)
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
	m := backup.Manifest{Format: 1, Kind: backup.KindMachine, Version: version.Version, Created: s.Now().UTC(), Parts: []string{"agent", "web"}}
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

// handleServerBackup streams one server's backup: manifest.json (kind
// "server"), history.jsonl (its sessions and traffic) and server/… (the
// agent's tar of the server).
func (s *Server) handleServerBackup(w http.ResponseWriter, r *http.Request) {
	var req BackupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	id := r.PathValue("server")
	info, err := s.agent(r).ServerInfo(ctx)
	if err != nil {
		s.agentError(w, err)
		return
	}
	src, err := s.agent(r).ServerBackup(ctx)
	if err != nil {
		s.agentError(w, err)
		return
	}
	defer func() { _ = src.Close() }()
	var hist bytes.Buffer
	if err := s.Stats.DB.ExportHistory(ctx, id, &hist); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, "history: "+err.Error())
		return
	}
	// The agent answers with the id on its machine; "<host>.<id>" names the host.
	host := ""
	if h, _, ok := strings.Cut(id, "."); ok {
		host = h
	}
	m := backup.Manifest{Format: 1, Kind: backup.KindServer, Version: version.Version, Created: s.Now().UTC(), Parts: []string{"server"},
		Server: &backup.Server{Type: info.Type, ID: info.ID, Name: info.Name, Host: host}}

	name := "trapdoor-" + strings.ReplaceAll(id, ".", "-") + "-" + s.Now().In(s.Location).Format("20060102-1504")
	name += map[bool]string{true: ".tdbackup", false: ".tar.gz"}[req.Passphrase != ""]
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	var out io.Writer = w
	var enc io.WriteCloser
	if req.Passphrase != "" {
		if enc, err = backup.NewEncrypter(w, req.Passphrase); err != nil {
			s.Log.Error("server backup", "err", err)
			return
		}
		out = enc
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	mj, _ := json.MarshalIndent(m, "", "  ")
	err = writeEntry(tw, "manifest.json", mj, m.Created)
	if err == nil {
		err = writeEntry(tw, "history.jsonl", hist.Bytes(), m.Created)
	}
	if err == nil {
		err = backup.Copy(tw, src, "server")
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err == nil && enc != nil {
		err = enc.Close()
	}
	if err != nil {
		s.Log.Error("server backup failed", "server", id, "err", err)
		return
	}
	s.audit(r, "server.backup", id)
}

// maxRestoreUpload bounds an uploaded server backup.
const maxRestoreUpload = 2 << 30

// handleRestoreServer restores a server from an uploaded backup
// (multipart: passphrase, host, replace, then file).
func (s *Server) handleRestoreServer(w http.ResponseWriter, r *http.Request) {
	if s.Backup == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "backups are not available")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRestoreUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "send the backup as a form upload")
		return
	}
	fields := map[string]string{}
	var file io.Reader
	for file == nil {
		part, err := mr.NextPart()
		if err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "no backup file in the upload")
			return
		}
		if part.FormName() == "file" {
			file = part
			break
		}
		b, _ := io.ReadAll(io.LimitReader(part, 4096))
		fields[part.FormName()] = string(b)
	}
	host, replace := fields["host"], fields["replace"] == "1" || fields["replace"] == "true"

	br := bufio.NewReader(file)
	var in io.Reader = br
	if start, _ := br.Peek(8); backup.Encrypted(start) {
		if fields["passphrase"] == "" {
			httpx.WriteError(w, http.StatusBadRequest, "this backup is encrypted: enter its passphrase")
			return
		}
		if in, err = backup.NewDecrypter(br, fields["passphrase"]); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	bad := func(err error) {
		if errors.Is(err, backup.ErrPassphrase) {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		httpx.WriteError(w, http.StatusBadRequest, "not a trapdoor server backup")
	}
	gz, err := gzip.NewReader(in)
	if err != nil {
		bad(err)
		return
	}
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" {
		bad(err)
		return
	}
	var m backup.Manifest
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil || m.Format != 1 {
		bad(err)
		return
	}
	if m.Kind != backup.KindServer || m.Server == nil {
		httpx.WriteError(w, http.StatusBadRequest, "this is a backup of a whole machine: restore it with `trapdoor restore` (see the guide)")
		return
	}
	// history.jsonl comes before the server's files.
	var hist []byte
	h, err = tr.Next()
	if err == nil && h.Name == "history.jsonl" {
		if hist, err = io.ReadAll(io.LimitReader(tr, 512<<20)); err == nil {
			h, err = tr.Next()
		}
	}
	if err != nil || !strings.HasPrefix(h.Name, "server/") {
		bad(err)
		return
	}

	// Stream server/… to the agent as its own tar.
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		err := func() error {
			for hd := h; ; {
				hd.Name = strings.TrimPrefix(hd.Name, "server/")
				if err := tw.WriteHeader(hd); err != nil {
					return err
				}
				if _, err := io.Copy(tw, tr); err != nil {
					return err
				}
				var err error
				if hd, err = tr.Next(); errors.Is(err, io.EOF) {
					return tw.Close()
				} else if err != nil {
					return err
				}
			}
		}()
		pw.CloseWithError(err)
	}()
	info, err := s.Backup.RestoreServer(r.Context(), host, pr, replace)
	_ = pr.CloseWithError(errors.New("restore finished"))
	if err != nil {
		s.agentError(w, err)
		return
	}
	added := 0
	if len(hist) > 0 {
		if added, err = s.Stats.DB.ImportHistory(r.Context(), info.ID, bytes.NewReader(hist)); err != nil {
			s.Log.Warn("restore: history not imported", "server", info.ID, "err", err)
		}
	}
	s.audit(r, "server.restore", info.ID)
	s.Log.Info("server restored", "server", info.ID, "history rows", added)
	httpx.WriteJSON(w, http.StatusCreated, info)
}
