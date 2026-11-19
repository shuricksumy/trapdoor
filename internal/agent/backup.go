package agent

import (
	"archive/tar"
	"net/http"
	"path/filepath"

	"github.com/shuricksumy/trapdoor/internal/backup"
)

// handleBackup streams the agent's directories as a tar: the first
// OpenVPN server's directory (with the added servers, the PKI and ccd when
// they are inside it), and the WireGuard and AmneziaWG directories.
// Runtime state (dumps, status files) is left out.
func (h *Host) handleBackup(w http.ResponseWriter, r *http.Request) {
	parts := h.backupDirs()
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	for _, p := range parts {
		if err := backup.AddDir(tw, p.Dir, p.Prefix, p.exclude...); err != nil {
			// Headers are sent: a broken tar is how the web side notices.
			h.log.Error("backup failed", "dir", p.Dir, "err", err)
			return
		}
	}
	if err := tw.Close(); err != nil {
		h.log.Error("backup failed", "err", err)
		return
	}
	h.log.Info("backup sent", "dirs", len(parts))
}

type backupDir struct {
	backup.Target
	exclude []string
}

// inside reports whether p is dir or below it.
func inside(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && filepath.IsLocal(rel)
}

// backupDirs are the directories in a backup and their names in it (the
// same names restore uses).
func (h *Host) backupDirs() []backupDir {
	h.mu.RLock()
	wg := h.cfg
	var ovpn *Server
	for _, m := range h.servers {
		if s, ok := m.b.(*Server); ok && s.extra == nil {
			ovpn = s
		}
	}
	h.mu.RUnlock()
	var out []backupDir
	if ovpn != nil {
		c := ovpn.cfg
		main := backupDir{Target: backup.Target{Prefix: "openvpn", Dir: c.OpenVPNDir}}
		for _, extra := range []struct{ prefix, dir string }{
			{"pki", c.PKIDir}, {"ccd", c.CCDDir}, {"openvpn-servers", c.OpenVPNServersDir},
		} {
			if extra.dir == "" {
				continue
			}
			if inside(extra.dir, c.OpenVPNDir) {
				continue // in the OpenVPN directory already
			}
			out = append(out, backupDir{Target: backup.Target{Prefix: extra.prefix, Dir: extra.dir}})
		}
		out = append([]backupDir{main}, out...)
	}
	if wg.WireGuard != "" && wg.WireGuard != "off" {
		out = append(out, backupDir{Target: backup.Target{Prefix: "wireguard", Dir: wg.WireGuardDir}})
		if wg.AmneziaWGDir != "" {
			out = append(out, backupDir{Target: backup.Target{Prefix: "amneziawg", Dir: wg.AmneziaWGDir}})
		}
	}
	return out
}

// RestoreTargets maps a backup's agent directories to this machine's
// (the TD_* settings of the agent that restores it).
func RestoreTargets(openvpnDir, pkiDir, ccdDir, serversDir, wireguardDir, amneziawgDir string) []backup.Target {
	t := []backup.Target{{Prefix: "openvpn", Dir: openvpnDir}}
	for _, x := range []struct{ prefix, dir string }{
		{"pki", pkiDir}, {"ccd", ccdDir}, {"openvpn-servers", serversDir},
		{"wireguard", wireguardDir}, {"amneziawg", amneziawgDir},
	} {
		if x.dir != "" {
			t = append(t, backup.Target{Prefix: x.prefix, Dir: x.dir})
		}
	}
	return t
}
