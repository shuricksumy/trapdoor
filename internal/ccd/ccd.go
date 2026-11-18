// Package ccd reads and writes per-client files in OpenVPN's
// client-config-dir. Several features edit the same file (2FA lines,
// network settings), so all edits go through one Store and its lock.
package ccd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/shuricksumy/tunnelward/internal/pki"
)

// Store is a client-config-dir.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// Read returns the file of name ("" if it does not exist).
func (s *Store) Read(name string) (string, error) {
	if !pki.ValidName(name) {
		return "", fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
}

// Edit replaces the file of name with fn(current). An empty result deletes it.
func (s *Store) Edit(name string, fn func(string) (string, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.Read(name)
	if err != nil {
		return err
	}
	out, err := fn(cur)
	if err != nil {
		return err
	}
	path := filepath.Join(s.Dir, name)
	if strings.TrimSpace(out) == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if out == cur {
		return nil
	}
	return writeKeepMode(path, []byte(out))
}

// All returns every client file (name → content); names that are not valid
// certificate names are skipped.
func (s *Store) All() (map[string]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !pki.ValidName(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = string(data)
	}
	return out, nil
}

// Remove deletes the file of name.
func (s *Store) Remove(name string) error {
	if !pki.ValidName(name) {
		return fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// writeKeepMode writes atomically and keeps the file's mode. New files are
// 0644 like openvpn-ui's: OpenVPN usually drops to "nobody" and must still
// read them (and run auth-verify as that user).
func writeKeepMode(path string, data []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tw-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}
