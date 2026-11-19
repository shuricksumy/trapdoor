package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shuricksumy/trapdoor/internal/agent"
	"github.com/shuricksumy/trapdoor/internal/backup"
	"github.com/shuricksumy/trapdoor/internal/config"
)

const restoreUsage = `usage: trapdoor restore agent|web [--host NAME] [--force] <backup file>

  agent   the VPN servers: OpenVPN (PKI, server.conf, ccd), WireGuard, AmneziaWG,
          into TD_OPENVPN_DIR, TD_WIREGUARD_DIR, TD_AMNEZIAWG_DIR (…)
  web     the web database (admins, traffic history, hosts) into TD_DB_PATH
  --host  restore another host's part of the backup (agent only)
  --force replace existing files (the old database is kept as *.before-restore-*)

Stop trapdoor first. An encrypted backup asks for its passphrase
(or reads TD_BACKUP_PASSPHRASE).
`

// restoreArgsError prints the usage and returns a short error.
func restoreArgsError() error {
	fmt.Fprint(os.Stderr, restoreUsage)
	return errors.New("restore: wrong arguments")
}

func runRestore(args []string) error {
	var part, host, file string
	force := false
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--force":
			force = true
		case a == "--host" && i+1 < len(args):
			host = args[i+1]
			i++
		case part == "" && (a == "agent" || a == "web"):
			part = a
		case file == "" && !strings.HasPrefix(a, "-"):
			file = a
		default:
			return restoreArgsError()
		}
	}
	if part == "" || file == "" || (host != "" && part != "agent") {
		return restoreArgsError()
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	br := bufio.NewReader(f)
	var r io.Reader = br
	if start, _ := br.Peek(8); backup.Encrypted(start) {
		pass, err := passphrase()
		if err != nil {
			return err
		}
		if r, err = backup.NewDecrypter(br, pass); err != nil {
			return err
		}
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		if errors.Is(err, backup.ErrPassphrase) {
			return err
		}
		return fmt.Errorf("not a trapdoor backup: %w", err)
	}
	tr := tar.NewReader(gz)
	h, err := tr.Next()
	if err != nil || h.Name != "manifest.json" {
		if errors.Is(err, backup.ErrPassphrase) {
			return err
		}
		return errors.New("not a trapdoor backup (no manifest.json)")
	}
	var m backup.Manifest
	if err := json.NewDecoder(io.LimitReader(tr, 1<<20)).Decode(&m); err != nil || m.Format != 1 {
		return errors.New("unknown backup format")
	}
	if m.Kind == backup.KindServer && m.Server != nil {
		return fmt.Errorf("this is a backup of one server (%s %s): restore it in the web UI, Add a server → From a backup", m.Server.Type, m.Server.ID)
	}
	fmt.Fprintf(os.Stderr, "backup of %s, %s (trapdoor %s), parts: %s\n", m.Hostname, m.Created.Local().Format(time.DateTime), m.Version, strings.Join(m.Parts, ", "))

	if part == "web" {
		return restoreWeb(tr, force)
	}
	prefix := "agent"
	if host != "" {
		prefix = "hosts/" + host
	}
	found := false
	for _, p := range m.Parts {
		found = found || p == prefix
	}
	if !found {
		return fmt.Errorf("the backup has no part %q", prefix)
	}
	ovpn, pki, ccd, servers, wg, awg := config.AgentDirs()
	targets := agent.RestoreTargets(ovpn, pki, ccd, servers, wg, awg)
	for i := range targets {
		targets[i].Prefix = prefix + "/" + targets[i].Prefix
	}
	if !force {
		for _, p := range []string{filepath.Join(ovpn, "server.conf"), filepath.Join(pki, "ca.crt")} {
			if _, err := os.Stat(p); err == nil {
				return fmt.Errorf("%s exists: this machine is set up already (use --force to replace its files)", p)
			}
		}
	}
	n, err := backup.ExtractFrom(tr, targets, force)
	if err != nil {
		return fmt.Errorf("after %d files: %w", n, err)
	}
	if n == 0 {
		return fmt.Errorf("no files for %s in the backup", prefix)
	}
	fmt.Fprintf(os.Stderr, "restored %d files into %s, %s, %s; start trapdoor again\n", n, ovpn, wg, awg)
	return nil
}

// restoreWeb puts web/trapdoor.db in place of TD_DB_PATH.
func restoreWeb(tr *tar.Reader, force bool) error {
	db := env("TD_DB_PATH", "./data/trapdoor.db")
	if _, err := os.Stat(db); err == nil && !force {
		return fmt.Errorf("%s exists (use --force to replace it; the old one is kept)", db)
	}
	dir := filepath.Dir(db)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(dir, ".restore-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if _, err := backup.ExtractFrom(tr, []backup.Target{{Prefix: "web", Dir: tmp}}, false); err != nil {
		return err
	}
	restored := filepath.Join(tmp, "trapdoor.db")
	if _, err := os.Stat(restored); err != nil {
		return errors.New("the backup has no web database")
	}
	if _, err := os.Stat(db); err == nil {
		old := db + ".before-restore-" + time.Now().Format("20060102-150405")
		if err := os.Rename(db, old); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "the old database is %s\n", old)
	}
	for _, x := range []string{"-wal", "-shm"} {
		if err := os.Remove(db + x); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.Rename(restored, db); err != nil {
		return err
	}
	if err := os.Chmod(db, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "restored the web database into %s; start trapdoor again\n", db)
	return nil
}

func passphrase() (string, error) {
	if p := os.Getenv("TD_BACKUP_PASSPHRASE"); p != "" {
		return p, nil
	}
	fmt.Fprint(os.Stderr, "backup passphrase: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", errors.New("no passphrase (set TD_BACKUP_PASSPHRASE or type it)")
	}
	return strings.TrimRight(line, "\r\n"), nil
}
