package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shuricksumy/trapdoor/internal/api"
	"github.com/shuricksumy/trapdoor/internal/httpx"
	"github.com/shuricksumy/trapdoor/internal/shaping"
)

// shaper keeps a server's speed limits (GET / PUT …/shaping): the speeds
// saved in <base>shaping.json, rendered with the clients' current
// addresses into <base>shaping.sh (tc commands, see package shaping) and
// applied like the access rules: by the agent itself (direct) or by the VPN
// container while the interface is up (file; it answers in
// <base>shaping.status: "ok|waiting|error <sha256> [message]").
type shaper struct {
	mode   string // FirewallDirect | FirewallFile | FirewallOff (TD_FIREWALL)
	iface  string // tun+ (the first OpenVPN server's tun<N>) or the device name
	ifb    string
	base   string               // file names: <base>shaping.json, .sh, .status
	waitFn func() time.Duration // file mode: how long to wait for the container
	addrs  func(ctx context.Context) (map[string]netip.Addr, error)
	log    *slog.Logger
	links  func() ([]net.Interface, error) // nil = net.Interfaces (tests replace it)

	mu      sync.Mutex
	applied string // direct: hash@device#ifindex of what is in the kernel
	lastErr string
}

// shapingWait: how long a change waits for the VPN container's answer
// (tests shorten it).
var shapingWait = 10 * time.Second

func (sh *shaper) path(ext string) string { return sh.base + "shaping." + ext }

func registerShaping(mux *http.ServeMux, sh *shaper) {
	mux.HandleFunc("GET /shaping", func(w http.ResponseWriter, r *http.Request) {
		st, err := sh.state(r.Context())
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	})
	mux.HandleFunc("PUT /shaping", func(w http.ResponseWriter, r *http.Request) {
		var in api.Shaping
		if !decodeBody(w, r, &in) {
			return
		}
		if err := validShaping(in); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if sh.mode == FirewallOff {
			httpx.WriteError(w, http.StatusConflict, "the agent does not manage the network (TD_FIREWALL=off)")
			return
		}
		st, err := sh.set(r.Context(), in)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, st)
	})
}

var shapingName = regexp.MustCompile(`^[^\x00-\x1f\x7f/\\]{1,128}$`)

func validShaping(in api.Shaping) error {
	check := func(what string, s api.Speed) error {
		if s.Down < 0 || s.Up < 0 || s.Down > shaping.MaxRate || s.Up > shaping.MaxRate {
			return fmt.Errorf("%s: speeds are 0 (unlimited) to %d kbit/s", what, shaping.MaxRate)
		}
		if s.Prio != "" && s.Prio != "high" && s.Prio != "low" {
			return fmt.Errorf("%s: priority is high, low or empty (normal)", what)
		}
		return nil
	}
	if err := check("default", in.Default); err != nil {
		return err
	}
	if in.Default.Block {
		return errors.New("default: blocking every client is not a speed limit")
	}
	if in.LinkDown < 0 || in.LinkUp < 0 || in.LinkDown > shaping.MaxRate || in.LinkUp > shaping.MaxRate {
		return fmt.Errorf("link: 0 (no priorities) to %d kbit/s", shaping.MaxRate)
	}
	if len(in.Clients) > 10000 {
		return errors.New("too many clients")
	}
	for name, s := range in.Clients {
		if !shapingName.MatchString(name) {
			return fmt.Errorf("%q is not a client name", name)
		}
		if err := check(name, s); err != nil {
			return err
		}
	}
	return nil
}

// load reads the saved speeds; ok is false when none were ever saved.
func (sh *shaper) load() (api.Shaping, bool, error) {
	var out api.Shaping
	data, err := os.ReadFile(sh.path("json"))
	if errors.Is(err, fs.ErrNotExist) {
		return api.Shaping{Clients: map[string]api.Speed{}}, false, nil
	}
	if err != nil {
		return out, false, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, false, fmt.Errorf("%s: %w", sh.path("json"), err)
	}
	if out.Clients == nil {
		out.Clients = map[string]api.Speed{}
	}
	return out, true, nil
}

// render builds the script for the speeds and the addresses known now.
func (sh *shaper) render(ctx context.Context, cfg api.Shaping) (string, map[string]string, error) {
	addrs, err := sh.addrs(ctx)
	if err != nil {
		return "", nil, err
	}
	names := make([]string, 0, len(addrs))
	for n := range addrs {
		names = append(names, n)
	}
	sort.Strings(names)
	clients := make([]shaping.Client, 0, len(names))
	shown := map[string]string{}
	for _, n := range names {
		sp, own := cfg.Clients[n]
		if !own {
			sp = cfg.Default
		}
		lim := shaping.Limit{Down: sp.Down, Up: sp.Up, Block: sp.Block, Prio: sp.Prio}
		clients = append(clients, shaping.Client{Name: n, Addr: addrs[n], Limit: lim})
		if lim.Limited() || cfg.LinkDown > 0 || cfg.LinkUp > 0 {
			shown[n] = addrs[n].String()
		}
	}
	script, _ := shaping.Render(sh.iface, sh.ifb, clients, shaping.Link{Down: cfg.LinkDown, Up: cfg.LinkUp})
	return script, shown, nil
}

func (sh *shaper) set(ctx context.Context, in api.Shaping) (api.ShapingState, error) {
	if in.Clients == nil {
		in.Clients = map[string]api.Speed{}
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	_, saved, err := sh.load()
	if err != nil {
		return api.ShapingState{}, err
	}
	empty := in.Default == (api.Speed{}) && len(in.Clients) == 0 && in.LinkDown == 0 && in.LinkUp == 0
	if !saved && empty { // nothing asked, nothing touched
		return api.ShapingState{Shaping: in, Mode: sh.mode, Applied: true, Addresses: map[string]string{}}, nil
	}
	data, _ := json.MarshalIndent(in, "", "  ")
	if err := writeAtomic(sh.path("json"), append(data, '\n'), 0o600); err != nil {
		return api.ShapingState{}, err
	}
	return sh.applyLocked(ctx, in, true)
}

// state reports the saved speeds and whether they are active, re-rendered
// with the addresses known now (nothing is written).
func (sh *shaper) state(ctx context.Context) (api.ShapingState, error) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	cfg, saved, err := sh.load()
	if err != nil {
		return api.ShapingState{}, err
	}
	st := api.ShapingState{Shaping: cfg, Mode: sh.mode, Addresses: map[string]string{}}
	if !saved {
		st.Applied = true
		return st, nil
	}
	script, addrs, err := sh.render(ctx, cfg)
	if err != nil {
		return st, err
	}
	st.Script, st.Addresses = script, addrs
	sh.report(&st, shaping.Hash(script))
	return st, nil
}

// applyLocked renders and applies; wait: in file mode, wait for the
// container's answer when the script changed.
func (sh *shaper) applyLocked(ctx context.Context, cfg api.Shaping, wait bool) (api.ShapingState, error) {
	st := api.ShapingState{Shaping: cfg, Mode: sh.mode, Addresses: map[string]string{}}
	script, addrs, err := sh.render(ctx, cfg)
	if err != nil {
		return st, err
	}
	st.Script, st.Addresses = script, addrs
	if sh.mode == FirewallOff {
		return st, nil
	}
	hash := shaping.Hash(script)
	if cur, _ := os.ReadFile(sh.path("sh")); string(cur) != script {
		if err := writeAtomic(sh.path("sh"), []byte(script), 0o644); err != nil {
			return st, err
		}
	}
	switch sh.mode {
	case FirewallDirect:
		sh.runDirect(ctx, hash)
	case FirewallFile:
		if wait {
			sh.waitFile(ctx, hash)
		}
	}
	sh.report(&st, hash)
	if st.Error != "" {
		sh.log.Error("speed limits not applied", "err", st.Error)
	}
	return st, nil
}

// report fills Applied / Waiting / Error for the script with this hash.
func (sh *shaper) report(st *api.ShapingState, hash string) {
	switch sh.mode {
	case FirewallDirect:
		dev, _ := sh.device()
		switch {
		case dev == "":
			st.Waiting = true
		case strings.HasPrefix(sh.applied, hash+"@"):
			st.Applied = true
		default:
			st.Error = sh.lastErr
		}
	case FirewallFile:
		state, known, msg := sh.fileStatus(hash)
		switch {
		case !known:
			st.Error = fwContainer + " has not applied the current speed limits yet"
		case state == "ok":
			st.Applied = true
		case state == "waiting":
			st.Waiting = true
		default:
			st.Error = fwContainer + " could not apply the speed limits: " + msg
		}
	}
}

func (sh *shaper) fileStatus(hash string) (state string, known bool, msg string) {
	data, err := os.ReadFile(sh.path("status"))
	if err != nil {
		return "", false, ""
	}
	f := strings.SplitN(strings.TrimSpace(string(data)), " ", 3)
	if len(f) < 2 || f[1] != hash {
		return "", false, ""
	}
	if len(f) == 3 {
		msg = f[2]
	}
	return f[0], true, msg
}

func (sh *shaper) waitFile(ctx context.Context, hash string) {
	var wait time.Duration
	if sh.waitFn != nil {
		wait = sh.waitFn()
	}
	if wait <= 0 {
		wait = shapingWait
	}
	deadline := time.Now().Add(wait)
	for {
		if _, known, _ := sh.fileStatus(hash); known || time.Now().After(deadline) || ctx.Err() != nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// device finds the interface: tun+ is the first tun<N> there is.
func (sh *shaper) device() (string, int) {
	links := sh.links
	if links == nil {
		links = net.Interfaces
	}
	ifs, err := links()
	if err != nil {
		return "", 0
	}
	for _, i := range ifs {
		if p, ok := strings.CutSuffix(sh.iface, "+"); ok {
			if n, found := strings.CutPrefix(i.Name, p); found && n != "" && strings.Trim(n, "0123456789") == "" {
				return i.Name, i.Index
			}
			continue
		}
		if i.Name == sh.iface {
			return i.Name, i.Index
		}
	}
	return "", 0
}

// runDirect runs the script unless the same one is already active on the
// same device (a recreated device has a new index, and no rules).
func (sh *shaper) runDirect(ctx context.Context, hash string) {
	dev, idx := sh.device()
	if dev == "" {
		sh.applied, sh.lastErr = "", ""
		return
	}
	key := fmt.Sprintf("%s@%s#%d", hash, dev, idx)
	if sh.applied == key {
		return
	}
	if err := sh.runScript(ctx, dev); err != nil {
		sh.applied, sh.lastErr = "", err.Error()
		return
	}
	sh.applied, sh.lastErr = key, ""
	sh.log.Info("speed limits applied", "device", dev)
}

func (sh *shaper) runScript(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", append([]string{sh.path("sh")}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tc: %v: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}

// refresh re-applies the saved speeds (direct mode at start, and after
// client addresses changed). Nothing happens before speeds were saved.
func (sh *shaper) refresh(ctx context.Context) {
	if sh.mode == FirewallOff {
		return
	}
	sh.mu.Lock()
	defer sh.mu.Unlock()
	cfg, saved, err := sh.load()
	if err != nil || !saved {
		return
	}
	if _, err := sh.applyLocked(ctx, cfg, false); err != nil {
		sh.log.Warn("speed limits refresh", "err", err)
	}
}

// remove takes the limits out (the server is gone). Best effort.
func (sh *shaper) remove(ctx context.Context) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.mode == FirewallDirect {
		if _, err := os.Stat(sh.path("sh")); err == nil {
			dev, _ := sh.device()
			if dev == "" {
				dev = "none"
			}
			_ = sh.runScript(ctx, dev, "clear")
		}
	}
	for _, ext := range []string{"json", "sh", "status"} {
		_ = os.Remove(sh.path(ext))
	}
	sh.applied = ""
}
