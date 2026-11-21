package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
)

// Scripts of a server: presets (rules trapdoor writes) and the server's own
// start / stop scripts, run by the vpn container (docker/vpn-entrypoint.sh)
// when the server's interface is up, as root, in this order:
//
//	<prefix>scripts-up.sh    presets, written by the agent
//	<prefix>up.sh            own script (web UI with TD_SCRIPTS=on, or the host)
//
// and on the way down <prefix>down.sh, then <prefix>scripts-down.sh.
// <prefix>scripts.env names the interface; <prefix>scripts.status is the vpn
// container's answer ("ok <sum>", "error <sum> <message>", "inactive").
// The prefix is "trapdoor-" for OpenVPN (in the server's directory) and
// "<id>." for WireGuard / AmneziaWG.
type scripts struct {
	dir, prefix string
	id, typ     string
	iface       string
	editable    bool // TD_SCRIPTS=on
	supported   bool // a vpn container runs them
}

const maxScript = 64 << 10

func (h scripts) path(name string) string { return filepath.Join(h.dir, h.prefix+name) }

// chain is the iptables chain of the presets (filter and nat), ≤ 28 characters.
func (h scripts) chain() string { return "TDH-" + h.id }

func readOptional(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// sum is what the vpn container computes over the files it runs:
// cat scripts-up.sh scripts-down.sh up.sh down.sh scripts.env | sha256sum.
func (h scripts) sum() (string, error) {
	hs := sha256.New()
	for _, n := range []string{"scripts-up.sh", "scripts-down.sh", "up.sh", "down.sh", "scripts.env"} {
		s, err := readOptional(h.path(n))
		if err != nil {
			return "", err
		}
		hs.Write([]byte(s))
	}
	return hex.EncodeToString(hs.Sum(nil)), nil
}

func (h scripts) get() (api.Scripts, error) {
	out := api.Scripts{Editable: h.editable, Supported: h.supported, Iface: h.iface,
		Files: []string{h.path("up.sh"), h.path("down.sh")}}
	if b, err := readOptional(h.path("scripts.json")); err != nil {
		return out, err
	} else if b != "" {
		if err := json.Unmarshal([]byte(b), &out.HookPresets); err != nil {
			return out, fmt.Errorf("%s: %w", h.path("scripts.json"), err)
		}
	}
	var err error
	if out.Up, err = readOptional(h.path("up.sh")); err != nil {
		return out, err
	}
	if out.Down, err = readOptional(h.path("down.sh")); err != nil {
		return out, err
	}
	status, err := readOptional(h.path("scripts.status"))
	if err != nil {
		return out, err
	}
	sum, err := h.sum()
	if err != nil {
		return out, err
	}
	f := strings.SplitN(strings.TrimSpace(status), " ", 3)
	switch {
	case !h.supported || !h.anything(out):
		out.State = ""
	case f[0] == "inactive":
		out.State = "inactive"
	case len(f) >= 2 && f[1] == sum && (f[0] == "ok" || f[0] == "error"):
		out.State = f[0]
		if len(f) == 3 {
			out.Message = f[2]
		}
	default:
		out.State = "pending"
	}
	return out, nil
}

func (h scripts) anything(s api.Scripts) bool {
	return s.DNSRedirect != "" || s.BlockDoT || s.Isolate || strings.TrimSpace(s.Up) != "" || strings.TrimSpace(s.Down) != ""
}

func checkScript(name, s string) error {
	if len(s) > maxScript {
		return fmt.Errorf("%s: at most %d KiB", name, maxScript>>10)
	}
	if strings.ContainsRune(s, 0) {
		return fmt.Errorf("%s: not a text file", name)
	}
	return nil
}

// set saves the presets (and the own scripts, when editable) and writes the
// files the vpn container runs.
func (h scripts) set(req api.Scripts) error {
	cur, err := h.get()
	if err != nil {
		return err
	}
	req.DNSRedirect = strings.TrimSpace(req.DNSRedirect)
	if req.DNSRedirect != "" {
		a, err := netip.ParseAddr(req.DNSRedirect)
		if err != nil || !a.Is4() {
			return badRequest("dns_redirect: an IPv4 address, e.g. 172.20.0.53")
		}
		req.DNSRedirect = a.String()
	}
	req.Up, req.Down = strings.ReplaceAll(req.Up, "\r\n", "\n"), strings.ReplaceAll(req.Down, "\r\n", "\n")
	if req.Up != cur.Up || req.Down != cur.Down {
		if !h.editable {
			return &httpError{http.StatusForbidden, fmt.Sprintf("own scripts are read-only on this node: edit %s and %s in this server's folder on the host (%s in the containers), or set TD_SCRIPTS=on for its agent",
				h.prefix+"up.sh", h.prefix+"down.sh", h.dir)}
		}
		for n, s := range map[string]string{"up": req.Up, "down": req.Down} {
			if err := checkScript(n, s); err != nil {
				return badRequest(err.Error())
			}
		}
	}
	b, _ := json.MarshalIndent(req.HookPresets, "", "  ")
	files := map[string]string{
		"scripts.json":    string(b) + "\n",
		"scripts-up.sh":   h.presetsUp(req.HookPresets),
		"scripts-down.sh": h.presetsDown(),
		"scripts.env":     fmt.Sprintf("SERVER_ID=%s\nTYPE=%s\nIFACE=%s\n", h.id, h.typ, h.iface),
	}
	if h.editable {
		files["up.sh"], files["down.sh"] = req.Up, req.Down
	}
	for name, content := range files {
		p := h.path(name)
		if (name == "up.sh" || name == "down.sh") && strings.TrimSpace(content) == "" {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			continue
		}
		if err := writeFileAtomic(p, []byte(content)); err != nil {
			return err
		}
	}
	return nil
}

// remove deletes the files (the vpn container then runs the saved stop
// scripts, as when the interface goes away).
func (h scripts) remove() {
	for _, n := range []string{"scripts.json", "scripts-up.sh", "scripts-down.sh", "scripts.env", "scripts.status", "up.sh", "down.sh"} {
		_ = os.Remove(h.path(n))
	}
}

// presetsUp renders the presets as rules in chain TDH-<id> (nat for the DNS
// redirect, filter for the rest), jumped to for the server's interface.
func (h scripts) presetsUp(p api.HookPresets) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	c := h.chain()
	w("#!/bin/sh")
	w("# trapdoor presets for %s (%s); written by the agent - change them in the web UI.", h.id, h.typ)
	w("set -e")
	if p.DNSRedirect != "" {
		w("iptables -t nat -N %s 2>/dev/null || iptables -t nat -F %s", c, c)
		w(`iptables -t nat -C PREROUTING -i "$IFACE" -j %s 2>/dev/null || iptables -t nat -I PREROUTING 1 -i "$IFACE" -j %s`, c, c)
		for _, proto := range []string{"udp", "tcp"} {
			w("iptables -t nat -A %s -p %s --dport 53 ! -d %s -j DNAT --to-destination %s:53", c, proto, p.DNSRedirect, p.DNSRedirect)
		}
	}
	if p.BlockDoT || p.Isolate {
		w("iptables -N %s 2>/dev/null || iptables -F %s", c, c)
		w(`iptables -C FORWARD -i "$IFACE" -j %s 2>/dev/null || iptables -I FORWARD 1 -i "$IFACE" -j %s`, c, c)
		if p.BlockDoT {
			w("iptables -A %s -p tcp --dport 853 -j REJECT --reject-with tcp-reset", c)
			w("iptables -A %s -p udp --dport 853 -j REJECT", c)
		}
		if p.Isolate {
			w(`iptables -A %s -o "$IFACE" -j REJECT`, c)
		}
	}
	return b.String()
}

// presetsDown removes everything presetsUp may have added.
func (h scripts) presetsDown() string {
	c := h.chain()
	return fmt.Sprintf(`#!/bin/sh
# trapdoor presets for %s: undone when the server stops; written by the agent.
iptables -t nat -D PREROUTING -i "$IFACE" -j %[2]s 2>/dev/null
iptables -t nat -F %[2]s 2>/dev/null && iptables -t nat -X %[2]s 2>/dev/null
iptables -D FORWARD -i "$IFACE" -j %[2]s 2>/dev/null
iptables -F %[2]s 2>/dev/null && iptables -X %[2]s 2>/dev/null
true
`, h.id, c)
}

func (h scripts) handleGet(w http.ResponseWriter, _ *http.Request) {
	s, err := h.get()
	if err != nil {
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s)
}

func (h scripts) handleSet(w http.ResponseWriter, r *http.Request) {
	if !h.supported {
		httpx.WriteError(w, http.StatusConflict, "scripts are run by the vpn container: this agent does not use one (TD_FIREWALL / TD_WIREGUARD=file)")
		return
	}
	var req api.Scripts
	if !decodeBody(w, r, &req) {
		return
	}
	if err := h.set(req); err != nil {
		var he *httpError
		if errors.As(err, &he) {
			httpx.WriteError(w, he.status, he.msg)
			return
		}
		httpx.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.handleGet(w, r)
}

// registerScripts serves a server's scripts; get is asked on every request.
func registerScripts(mux *http.ServeMux, get func() scripts) {
	mux.HandleFunc("GET /scripts", func(w http.ResponseWriter, r *http.Request) { get().handleGet(w, r) })
	mux.HandleFunc("PUT /scripts", func(w http.ResponseWriter, r *http.Request) { get().handleSet(w, r) })
}

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func badRequest(msg string) error { return &httpError{http.StatusBadRequest, msg} }
