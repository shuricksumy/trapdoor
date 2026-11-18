package pki

import (
	"os"
	"path/filepath"
	"syscall"
)

// fileLock is an advisory flock(2) on a file inside the PKI directory, so two
// trapdoor processes (or a future easyrsa wrapper) never write at once.
type fileLock struct{ f *os.File }

func lockFile(dir string, exclusive bool) (*fileLock, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".trapdoor.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &fileLock{f: f}, nil
}

func (l *fileLock) unlock() {
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}
