package serverconf

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrConflict means server.conf changed since the caller read it.
var ErrConflict = errors.New("server.conf was changed in the meantime; reload and try again")

const keepBackups = 10

// File is server.conf with backups and change detection.
type File struct {
	Path string
	Now  func() time.Time // nil = time.Now

	mu      sync.Mutex
	running string // content OpenVPN is assumed to run with
	started bool
}

// Version identifies file content (for optimistic locking).
func Version(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:8])
}

func (f *File) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Read returns the content and its version. At the first read the content
// is taken as what OpenVPN currently runs with.
func (f *File) Read() (raw, version string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readLocked()
}

func (f *File) readLocked() (string, string, error) {
	data, err := os.ReadFile(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = nil, nil
	}
	if err != nil {
		return "", "", err
	}
	if !f.started {
		f.running, f.started = string(data), true
	}
	return string(data), Version(string(data)), nil
}

// Pending reports whether the file differs from what OpenVPN runs with.
func (f *File) Pending() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _, err := f.readLocked()
	return err == nil && raw != f.running
}

// Running returns the content OpenVPN is assumed to run with.
func (f *File) Running() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, _, _ = f.readLocked()
	return f.running
}

// MarkRunning records that OpenVPN was (re)started with raw.
func (f *File) MarkRunning(raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.running, f.started = raw, true
}

// Write replaces the file if it still has baseVersion, keeping a backup of
// the previous content. The file mode is kept (new files: 0644).
func (f *File) Write(raw, baseVersion string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ver, err := f.readLocked()
	if err != nil {
		return "", err
	}
	if baseVersion != ver {
		return "", ErrConflict
	}
	if raw == cur {
		return ver, nil
	}
	if cur != "" {
		if err := f.backup(cur); err != nil {
			return "", fmt.Errorf("backup: %w", err)
		}
	}
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(f.Path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp := f.Path + ".tw-tmp"
	if err := os.WriteFile(tmp, []byte(raw), mode); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return "", err
	}
	return Version(raw), nil
}

func (f *File) backupDir() string { return filepath.Join(filepath.Dir(f.Path), "tunnelward-backups") }

var backupName = regexp.MustCompile(`^server\.conf\.\d{8}-\d{6}(-\d+)?$`)

func (f *File) backup(raw string) error {
	dir := f.backupDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	base := "server.conf." + f.now().UTC().Format("20060102-150405")
	name := base
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); errors.Is(err, fs.ErrNotExist) {
			break
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
		return err
	}
	list, err := f.backupsLocked()
	if err != nil {
		return err
	}
	for _, b := range list[min(len(list), keepBackups):] {
		_ = os.Remove(filepath.Join(dir, b.Name))
	}
	return nil
}

// Backup is one saved previous version.
type Backup struct {
	Name string    `json:"name"`
	Time time.Time `json:"time"`
	Size int64     `json:"size"`
}

// Backups lists saved versions, newest first.
func (f *File) Backups() ([]Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.backupsLocked()
}

func (f *File) backupsLocked() ([]Backup, error) {
	entries, err := os.ReadDir(f.backupDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []Backup{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		if !backupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		ts := strings.TrimPrefix(e.Name(), "server.conf.")
		t, _ := time.Parse("20060102-150405", ts[:15])
		out = append(out, Backup{Name: e.Name(), Time: t, Size: info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// ReadBackup returns the content of a backup.
func (f *File) ReadBackup(name string) (string, error) {
	if !backupName.MatchString(name) {
		return "", fmt.Errorf("%w: unknown backup %q", ErrInvalid, name)
	}
	data, err := os.ReadFile(filepath.Join(f.backupDir(), name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: unknown backup %q", ErrInvalid, name)
	}
	return string(data), err
}
