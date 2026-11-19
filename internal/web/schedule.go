package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// Scheduled backups: everything (and optionally every server on its own)
// daily or weekly into TD_BACKUP_DIR on the web side, encrypted with
// TD_BACKUP_PASSPHRASE, the newest Keep files of each kind kept.

const (
	settingBackupSchedule = "backup.schedule"
	settingBackupStatus   = "backup.status" // the last run, scheduled or not
	settingBackupSlot     = "backup.slot"   // when the last scheduled run started
)

// BackupSchedule is when scheduled backups run (PUT /api/v1/backups/schedule).
type BackupSchedule struct {
	// Every is "off", "daily" or "weekly".
	Every string `json:"every"`
	// Weekday for "weekly": 0 = Sunday … 6 = Saturday.
	Weekday int `json:"weekday"`
	// Hour of the day (0-23) in TD_TZ.
	Hour int `json:"hour"`
	// Keep is how many files of each kind stay (older ones are deleted).
	Keep int `json:"keep"`
	// Servers also writes one file per server (easy to restore or move one).
	Servers bool `json:"servers"`
	// Since is when the schedule was saved: slots before it do not run.
	Since time.Time `json:"since"`
}

// BackupStatus is the outcome of the last scheduled (or "run now") backup.
type BackupStatus struct {
	Time     time.Time `json:"time"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Files    []string  `json:"files,omitempty"`
	Manual   bool      `json:"manual,omitempty"`
	Duration float64   `json:"duration_seconds"`
}

// BackupFile is a file in the backup folder.
type BackupFile struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	Time time.Time `json:"time"`
}

// BackupsInfo is GET /api/v1/backups.
type BackupsInfo struct {
	Schedule  BackupSchedule `json:"schedule"`
	Last      *BackupStatus  `json:"last,omitempty"`
	Next      *time.Time     `json:"next,omitempty"`
	Dir       string         `json:"dir"`
	Encrypted bool           `json:"encrypted"`
	Files     []BackupFile   `json:"files"`
	Running   bool           `json:"running"`
}

var defaultSchedule = BackupSchedule{Every: "off", Weekday: 0, Hour: 3, Keep: 7}

// backupName: trapdoor-all-<time> or trapdoor-server-<id>-<time>.
var backupName = regexp.MustCompile(`^(trapdoor-(?:all|server-[a-z0-9][a-z0-9.-]*))-(\d{8}-\d{6})\.(tdbackup|tar\.gz)$`)

// backupRunner serializes runs (schedule and "run now").
type backupRunner struct {
	mu      sync.Mutex
	running bool
}

func (s *Server) schedule(ctx context.Context) (BackupSchedule, error) {
	sc := defaultSchedule
	_, err := s.Stats.DB.Setting(ctx, settingBackupSchedule, &sc)
	return sc, err
}

// lastSlot is the most recent scheduled time at or before now.
func lastSlot(sc BackupSchedule, now time.Time) time.Time {
	t := time.Date(now.Year(), now.Month(), now.Day(), sc.Hour, 0, 0, 0, now.Location())
	if sc.Every == "weekly" {
		t = t.AddDate(0, 0, -((int(now.Weekday()) - sc.Weekday + 7) % 7))
	}
	if t.After(now) {
		if sc.Every == "weekly" {
			t = t.AddDate(0, 0, -7)
		} else {
			t = t.AddDate(0, 0, -1)
		}
	}
	return t
}

// nextSlot is the first scheduled time after now.
func nextSlot(sc BackupSchedule, now time.Time) time.Time {
	t := lastSlot(sc, now)
	if sc.Every == "weekly" {
		return t.AddDate(0, 0, 7)
	}
	return t.AddDate(0, 0, 1)
}

// RunBackups runs scheduled backups until ctx ends. A slot missed while the
// web UI was down runs when it is back.
func (s *Server) RunBackups(ctx context.Context) {
	if s.BackupDir == "" || s.Backup == nil {
		return
	}
	if s.BackupPassphrase == "" {
		s.Log.Warn("TD_BACKUP_PASSPHRASE is not set: scheduled backups are not encrypted")
	}
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.backupIfDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) backupIfDue(ctx context.Context) {
	sc, err := s.schedule(ctx)
	if err != nil || sc.Every == "off" {
		return
	}
	now := s.Now().In(s.Location)
	slot := lastSlot(sc, now)
	if !s.slotDue(ctx, sc, slot) {
		return
	}
	_, _ = s.runBackup(ctx, sc, false)
}

// slotDue: the slot is after the schedule was saved and has not run yet.
func (s *Server) slotDue(ctx context.Context, sc BackupSchedule, slot time.Time) bool {
	var done time.Time
	_, _ = s.Stats.DB.Setting(ctx, settingBackupSlot, &done)
	return !slot.Before(sc.Since) && slot.After(done)
}

// runBackup writes the files, prunes old ones and stores the outcome.
func (s *Server) runBackup(ctx context.Context, sc BackupSchedule, manual bool) (BackupStatus, error) {
	s.backups.mu.Lock()
	if s.backups.running {
		s.backups.mu.Unlock()
		return BackupStatus{}, errBackupRunning
	}
	s.backups.running = true
	s.backups.mu.Unlock()
	defer func() {
		s.backups.mu.Lock()
		s.backups.running = false
		s.backups.mu.Unlock()
	}()

	start := s.Now()
	st := BackupStatus{Time: start.UTC(), Manual: manual}
	if !manual {
		// Recorded first: a failing backup is not retried every minute.
		if err := s.Stats.DB.SetSetting(ctx, settingBackupSlot, start.UTC()); err != nil {
			s.Log.Error("backup schedule", "err", err)
		}
	}
	var errs []string
	if err := os.MkdirAll(s.BackupDir, 0o700); err != nil {
		errs = append(errs, err.Error())
	} else {
		stamp := start.In(s.Location).Format("20060102-150405")
		ext := backupExt(s.BackupPassphrase)
		name := "trapdoor-all-" + stamp + ext
		if err := s.writeBackupFile(name, func(w io.Writer) error {
			b, err := s.prepareMachineBackup(ctx)
			if err != nil {
				return err
			}
			defer b.Close()
			return s.writeMachineBackup(ctx, b, w, s.BackupPassphrase)
		}); err != nil {
			errs = append(errs, "everything: "+err.Error())
		} else {
			st.Files = append(st.Files, name)
		}
		if sc.Servers {
			info, err := s.Agent.Info(ctx)
			if err != nil {
				errs = append(errs, "servers: "+err.Error())
			}
			for _, sv := range info.Servers {
				name := "trapdoor-server-" + sv.ID + "-" + stamp + ext
				if err := s.writeBackupFile(name, func(w io.Writer) error {
					b, err := s.prepareServerBackup(ctx, sv.ID)
					if err != nil {
						return err
					}
					defer func() { _ = b.src.Close() }()
					return writeServerBackup(b, w, s.BackupPassphrase)
				}); err != nil {
					errs = append(errs, sv.ID+": "+err.Error())
				} else {
					st.Files = append(st.Files, name)
				}
			}
		}
		if err := s.pruneBackups(max(sc.Keep, 1)); err != nil {
			errs = append(errs, "cleaning up: "+err.Error())
		}
	}
	st.OK = len(errs) == 0
	st.Error = strings.Join(errs, "; ")
	st.Duration = s.Now().Sub(start).Seconds()
	if err := s.Stats.DB.SetSetting(ctx, settingBackupStatus, st); err != nil {
		s.Log.Error("backup status", "err", err)
	}
	if st.OK {
		s.Log.Info("backup done", "files", len(st.Files), "dir", s.BackupDir, "seconds", int(st.Duration))
	} else {
		s.Log.Error("backup failed", "err", st.Error, "files", len(st.Files))
	}
	if !st.OK {
		return st, errors.New(st.Error)
	}
	return st, nil
}

var errBackupRunning = errors.New("a backup is running already")

// writeBackupFile writes name in the backup folder through a temporary
// file, so a failed or interrupted backup leaves no partial file behind.
func (s *Server) writeBackupFile(name string, write func(io.Writer) error) error {
	tmp, err := os.CreateTemp(s.BackupDir, ".partial-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(s.BackupDir, name))
}

// pruneBackups keeps the newest keep files of each kind (everything, each
// server); files with other names are left alone.
func (s *Server) pruneBackups(keep int) error {
	files, err := s.backupFiles()
	if err != nil {
		return err
	}
	byKind := map[string][]string{}
	for _, f := range files {
		m := backupName.FindStringSubmatch(f.Name)
		byKind[m[1]] = append(byKind[m[1]], f.Name)
	}
	var errs []error
	for _, names := range byKind {
		sort.Sort(sort.Reverse(sort.StringSlice(names))) // the time stamp sorts
		for _, n := range names[min(keep, len(names)):] {
			if err := os.Remove(filepath.Join(s.BackupDir, n)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// backupFiles lists the backup files, newest first.
func (s *Server) backupFiles() ([]BackupFile, error) {
	entries, err := os.ReadDir(s.BackupDir)
	if errors.Is(err, os.ErrNotExist) {
		return []BackupFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	files := []BackupFile{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !backupName.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, BackupFile{Name: e.Name(), Size: fi.Size(), Time: fi.ModTime().UTC()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Time.After(files[j].Time) })
	return files, nil
}

func (s *Server) registerScheduleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/backups", s.handleBackups)
	mux.HandleFunc("PUT /api/v1/backups/schedule", s.handleSetSchedule)
	mux.HandleFunc("POST /api/v1/backups/run", s.handleRunBackup)
	mux.HandleFunc("GET /api/v1/backups/files/{name}", s.handleBackupFile)
	mux.HandleFunc("DELETE /api/v1/backups/files/{name}", s.handleDeleteBackupFile)
}

func (s *Server) backupsAvailable(w http.ResponseWriter) bool {
	if s.BackupDir == "" || s.Backup == nil {
		httpx.WriteError(w, http.StatusNotImplemented, "scheduled backups are off (TD_BACKUP_DIR=-)")
		return false
	}
	return true
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	if !s.backupsAvailable(w) {
		return
	}
	ctx := r.Context()
	sc, err := s.schedule(ctx)
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := BackupsInfo{Schedule: sc, Dir: s.BackupDir, Encrypted: s.BackupPassphrase != ""}
	var last BackupStatus
	if ok, _ := s.Stats.DB.Setting(ctx, settingBackupStatus, &last); ok {
		out.Last = &last
	}
	if sc.Every != "off" {
		now := s.Now().In(s.Location)
		next := nextSlot(sc, now)
		// A slot that is due (or was missed) runs within a minute.
		if s.slotDue(ctx, sc, lastSlot(sc, now)) {
			next = now
		}
		out.Next = &next
	}
	if out.Files, err = s.backupFiles(); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.backups.mu.Lock()
	out.Running = s.backups.running
	s.backups.mu.Unlock()
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Server) handleSetSchedule(w http.ResponseWriter, r *http.Request) {
	if !s.backupsAvailable(w) {
		return
	}
	var sc BackupSchedule
	if !decodeJSON(w, r, &sc) {
		return
	}
	switch {
	case sc.Every != "off" && sc.Every != "daily" && sc.Every != "weekly":
		httpx.WriteError(w, http.StatusBadRequest, "every: off, daily or weekly")
		return
	case sc.Hour < 0 || sc.Hour > 23:
		httpx.WriteError(w, http.StatusBadRequest, "hour: 0-23")
		return
	case sc.Weekday < 0 || sc.Weekday > 6:
		httpx.WriteError(w, http.StatusBadRequest, "weekday: 0 (Sunday) - 6")
		return
	case sc.Keep < 1 || sc.Keep > 365:
		httpx.WriteError(w, http.StatusBadRequest, "keep: 1-365 files of each kind")
		return
	}
	sc.Since = s.Now().UTC()
	if err := s.Stats.DB.SetSetting(r.Context(), settingBackupSchedule, sc); err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "backup.schedule", sc.Every)
	httpx.WriteJSON(w, http.StatusOK, sc)
}

func (s *Server) handleRunBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupsAvailable(w) {
		return
	}
	sc, err := s.schedule(r.Context())
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Not tied to the request: closing the page does not stop the backup.
	st, err := s.runBackup(context.WithoutCancel(r.Context()), sc, true)
	if errors.Is(err, errBackupRunning) {
		httpx.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	s.audit(r, "backup.run", fmt.Sprint(len(st.Files)))
	httpx.WriteJSON(w, http.StatusOK, st)
}

func (s *Server) backupPath(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.backupsAvailable(w) {
		return "", false
	}
	name := r.PathValue("name")
	if !backupName.MatchString(name) {
		httpx.WriteError(w, http.StatusNotFound, "no such backup")
		return "", false
	}
	return filepath.Join(s.BackupDir, name), true
}

func (s *Server) handleBackupFile(w http.ResponseWriter, r *http.Request) {
	path, ok := s.backupPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		httpx.WriteError(w, http.StatusNotFound, "no such backup")
		return
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		httpx.WriteError(w, http.StatusNotFound, "no such backup")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(path)+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

func (s *Server) handleDeleteBackupFile(w http.ResponseWriter, r *http.Request) {
	path, ok := s.backupPath(w, r)
	if !ok {
		return
	}
	if err := os.Remove(path); err != nil {
		httpx.WriteError(w, http.StatusNotFound, "no such backup")
		return
	}
	s.audit(r, "backup.delete", filepath.Base(path))
	w.WriteHeader(http.StatusNoContent)
}
