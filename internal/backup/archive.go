package backup

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Manifest is manifest.json at the top of a backup.
type Manifest struct {
	Format   int       `json:"format"` // 1
	Version  string    `json:"version"`
	Created  time.Time `json:"created"`
	Hostname string    `json:"hostname"`
	// Parts in the archive: "agent", "web", "hosts/<name>".
	Parts []string `json:"parts"`
	// Errors: parts that could not be backed up (e.g. a host was down).
	Errors []string `json:"errors,omitempty"`
}

// skip reports files that are state of the running system, not settings:
// rebuilt or rewritten by trapdoor and the VPN container.
func skip(rel string, d fs.DirEntry) bool {
	base := d.Name()
	if d.Type()&(fs.ModeSocket|fs.ModeNamedPipe|fs.ModeDevice) != 0 {
		return true
	}
	for _, suffix := range []string{".td-tmp", ".tmp", ".dump", ".error", ".sync", ".fwstatus", ".strip"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return base == "trapdoor-firewall.status" || base == "openvpn-status.log"
}

// AddDir writes the files below dir into tw under prefix (e.g.
// "agent/openvpn"). A missing dir adds nothing. exclude are paths below dir
// to leave out (e.g. a directory that is added under its own prefix).
func AddDir(tw *tar.Writer, dir, prefix string, exclude ...string) error {
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		for _, x := range exclude {
			if rel == x || strings.HasPrefix(rel, x+string(filepath.Separator)) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if rel == "." || skip(rel, d) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = path.Join(prefix, filepath.ToSlash(rel))
		if d.IsDir() {
			h.Name += "/"
		}
		h.Uname, h.Gname = "", ""
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		// The size is fixed in the header: a file that grew is cut, one
		// that shrank fails the backup.
		_, err = io.CopyN(tw, f, info.Size())
		return err
	})
}

// Copy copies the entries of an archive read from r into tw, under prefix.
func Copy(tw *tar.Writer, r io.Reader, prefix string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		h.Name = path.Join(prefix, h.Name)
		if h.Typeflag == tar.TypeDir {
			h.Name += "/"
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
}

// Target is where one directory of a backup goes when restored.
type Target struct {
	Prefix string // in the archive, e.g. "agent/openvpn"
	Dir    string // on disk
}

// Extract restores the entries below each target's prefix into its dir.
// Entries that would leave the dir, and device files, are refused. With
// force, existing files are replaced; without it, an existing file stops
// the restore. It returns how many files were written.
func Extract(r io.Reader, targets []Target, force bool) (int, error) {
	return ExtractFrom(tar.NewReader(r), targets, force)
}

// ExtractFrom is Extract for an archive already being read (e.g. after
// its manifest).
func ExtractFrom(tr *tar.Reader, targets []Target, force bool) (int, error) {
	n := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		name := strings.TrimSuffix(h.Name, "/")
		var dst, rel string
		for _, t := range targets {
			if r, ok := strings.CutPrefix(name, t.Prefix+"/"); ok {
				rel = filepath.FromSlash(r)
				if !filepath.IsLocal(rel) {
					return n, fmt.Errorf("%s: path leaves the target directory", h.Name)
				}
				dst = filepath.Join(t.Dir, rel)
				break
			}
		}
		if dst == "" {
			continue // another part of the backup
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, fs.FileMode(h.Mode).Perm()|0o700); err != nil {
				return n, err
			}
		case tar.TypeReg:
			if err := writeFile(dst, tr, fs.FileMode(h.Mode).Perm(), force); err != nil {
				return n, err
			}
			n++
		case tar.TypeSymlink:
			// Only links that stay inside the same target directory.
			if filepath.IsAbs(h.Linkname) || !filepath.IsLocal(filepath.Join(filepath.Dir(rel), filepath.FromSlash(h.Linkname))) {
				return n, fmt.Errorf("%s: link points outside the backup", h.Name)
			}
			if _, err := os.Lstat(dst); err == nil {
				if !force {
					return n, fmt.Errorf("%s exists (use --force to replace)", dst)
				}
				_ = os.Remove(dst)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return n, err
			}
			if err := os.Symlink(h.Linkname, dst); err != nil {
				return n, err
			}
		default:
			return n, fmt.Errorf("%s: unexpected entry type %c", h.Name, h.Typeflag)
		}
	}
}

func writeFile(dst string, r io.Reader, perm fs.FileMode, force bool) error {
	if fi, err := os.Lstat(dst); err == nil {
		if !force {
			return fmt.Errorf("%s exists (use --force to replace)", dst)
		}
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s exists and is not a file", dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".td-tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
