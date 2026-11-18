package clientauth

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/shuricksumy/tunnelward/internal/pki"
)

// Store reads and writes the 2FA lines of ccd files in Dir.
type Store struct {
	Dir string
	mu  sync.Mutex
}

// Get returns the settings of name (empty settings if it has no ccd file).
func (s *Store) Get(name string) (Settings, error) {
	data, err := s.read(name)
	if err != nil {
		return Settings{}, err
	}
	return Parse(data)
}

// Update changes the settings of name with fn, keeping all other lines.
func (s *Store) Update(name string, fn func(*Settings) error) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.read(name)
	if err != nil {
		return Settings{}, err
	}
	cur, err := Parse(data)
	if err != nil {
		return Settings{}, err
	}
	if err := fn(&cur); err != nil {
		return Settings{}, err
	}
	out := Apply(data, cur)
	path := filepath.Join(s.Dir, name)
	if out == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Settings{}, err
		}
		return cur, nil
	}
	return cur, writeKeepMode(path, []byte(out))
}

// Remove deletes the client's ccd file (when the client is deleted).
func (s *Store) Remove(name string) error {
	if !pki.ValidName(name) {
		return fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	err := os.Remove(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func (s *Store) read(name string) (string, error) {
	if !pki.ValidName(name) {
		return "", fmt.Errorf("%w: %q", pki.ErrInvalidName, name)
	}
	data, err := os.ReadFile(filepath.Join(s.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return string(data), err
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
