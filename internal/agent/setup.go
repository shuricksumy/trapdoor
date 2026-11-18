package agent

import (
	"bufio"
	"context"
	"crypto/elliptic"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/api"
	"github.com/shuricksumy/tunnelward/internal/httpx"
	"github.com/shuricksumy/tunnelward/internal/pki"
	"github.com/shuricksumy/tunnelward/internal/serverconf"
	"github.com/shuricksumy/tunnelward/internal/setup"
)

// serverCertName is the common name of the server certificate the setup
// creates; client profiles check it with verify-x509-name.
const serverCertName = "server"

func (s *Server) registerSetupRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/setup", s.handleSetupStatus)
	mux.HandleFunc("POST /v1/setup", s.handleSetup)
}

func (s *Server) setupStatus(ctx context.Context) api.SetupStatus {
	st := api.SetupStatus{HasPKI: s.PKI() != nil}
	if _, err := os.Stat(s.conf.Path); err == nil {
		st.HasServerConf = true
	}
	st.Needed = !st.HasServerConf
	if p := s.PKI(); p != nil {
		if ca, err := p.CAInfo(ctx); err == nil {
			st.CAName = ca.Name
		}
	}
	st.FixedPort, st.FixedProto = s.cfg.SetupPort, s.cfg.SetupProto
	st.Defaults.Port, st.Defaults.Proto, st.Defaults.PublicPort = st.FixedPort, st.FixedProto, s.cfg.SetupPublicPort
	setup.Defaults(&st.Defaults)
	if st.CAName != "" {
		st.Defaults.CAName = st.CAName
	}
	return st
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.setupStatus(r.Context()))
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var req api.SetupRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.Port == 0 {
		req.Port = s.cfg.SetupPort
	}
	if req.Proto == "" {
		req.Proto = s.cfg.SetupProto
	}
	setup.Defaults(&req)
	if p := s.cfg.SetupPort; p != 0 && req.Port != p {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("OpenVPN must listen on port %d: the container publishes only that port (change VPN_PORT for the public port)", p))
		return
	}
	if p := s.cfg.SetupProto; p != "" && req.Proto != p {
		httpx.WriteError(w, http.StatusBadRequest, fmt.Sprintf("the container publishes %s only (set VPN_PROTO=%s in .env and recreate the containers to change it)", strings.ToUpper(p), req.Proto))
		return
	}
	if err := setup.Validate(req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// One setup (or restart) at a time.
	s.restartMu.Lock()
	defer s.restartMu.Unlock()
	if s.setupStatus(r.Context()).HasServerConf {
		httpx.WriteError(w, http.StatusConflict, "the server is already set up; change it on the Server page")
		return
	}
	res, status, err := s.runSetup(r.Context(), req)
	if err != nil {
		s.log.Error("setup failed", "err", err)
		httpx.WriteError(w, status, err.Error())
		return
	}
	s.log.Info("setup done", "running", res.Running, "port", req.Port, "proto", req.Proto)
	httpx.WriteJSON(w, http.StatusOK, res)
}

func (s *Server) runSetup(ctx context.Context, req setup.Request) (api.SetupResult, int, error) {
	fail := func(status int, format string, a ...any) (api.SetupResult, int, error) {
		return api.SetupResult{}, status, fmt.Errorf(format, a...)
	}
	dir := s.cfg.OpenVPNDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail(http.StatusInternalServerError, "create %s: %v", dir, err)
	}

	// 1. Certificate authority: reuse an existing one.
	p := s.PKI()
	if p == nil {
		kt, err := keyType(req.KeyType)
		if err != nil {
			return fail(http.StatusBadRequest, "%v", err)
		}
		store, err := pki.Init(s.cfg.PKIDir, pki.InitOptions{CommonName: req.CAName, KeyType: kt},
			pki.Options{CAPassphrase: s.cfg.CAPassphrase, CRLCopies: s.cfg.CRLCopies})
		if err != nil {
			return fail(http.StatusInternalServerError, "create the certificate authority: %v", err)
		}
		s.setPKI(store)
		p = store
		s.log.Info("setup: created the certificate authority", "name", req.CAName, "key", req.KeyType)
	}

	// 2. Server certificate: reuse a valid one.
	c, err := p.Get(ctx, serverCertName)
	switch {
	case err == nil && c.Status == pki.StatusValid && c.Kind == pki.KindServer:
	case err == nil || errors.Is(err, pki.ErrNotFound):
		if err == nil {
			// A client or a revoked/expired certificate has the name.
			return fail(http.StatusConflict, "a %s certificate named %q exists; delete it first", c.Status, serverCertName)
		}
		if _, err := p.Issue(ctx, pki.IssueRequest{Name: serverCertName, Kind: pki.KindServer}); err != nil {
			return fail(http.StatusInternalServerError, "issue the server certificate: %v", err)
		}
	default:
		return fail(http.StatusInternalServerError, "read the server certificate: %v", err)
	}
	if err := p.GenCRL(ctx); err != nil {
		return fail(http.StatusInternalServerError, "create the CRL: %v", err)
	}

	// 3. Control channel key.
	paths, err := s.setupPaths(req)
	if err != nil {
		return fail(http.StatusInternalServerError, "%v", err)
	}
	if req.TLS != setup.TLSNone {
		if err := s.genTLSKey(ctx, req.TLS, filepath.Join(dir, paths.TLSKey)); err != nil {
			return fail(http.StatusInternalServerError, "create the %s key: %v", req.TLS, err)
		}
	}

	// 4. Client config dir, management password.
	if err := os.MkdirAll(s.cfg.CCDDir, 0o755); err != nil {
		return fail(http.StatusInternalServerError, "create %s: %v", s.cfg.CCDDir, err)
	}
	if s.cfg.MgmtPassword != "" {
		if err := os.WriteFile(filepath.Join(dir, "management.pw"), []byte(s.cfg.MgmtPassword+"\n"), 0o600); err != nil {
			return fail(http.StatusInternalServerError, "write the management password: %v", err)
		}
		paths.Management += " management.pw"
	}

	// 5. Check, then write.
	conf := setup.ServerConf(req, paths, time.Now())
	problems := serverconf.Check(ctx, conf, dir, s.cfg.OpenVPNBin)
	if serverconf.HasErrors(problems) {
		var msgs []string
		for _, pr := range problems {
			if pr.Error {
				msgs = append(msgs, pr.Message)
			}
		}
		return fail(http.StatusUnprocessableEntity, "the generated server.conf did not pass the check: %s", strings.Join(msgs, "; "))
	}
	tmpl := filepath.Join(dir, "client-template.txt")
	if old, err := os.ReadFile(tmpl); err == nil {
		_ = os.WriteFile(tmpl+".bak", old, 0o644)
	}
	if err := os.WriteFile(tmpl, []byte(setup.ClientTemplate(req)), 0o644); err != nil {
		return fail(http.StatusInternalServerError, "write client-template.txt: %v", err)
	}
	if _, err := s.conf.Write(conf, serverconf.Version("")); err != nil {
		return fail(http.StatusInternalServerError, "write server.conf: %v", err)
	}

	// 6. OpenVPN under a supervisor (Docker) starts once server.conf exists.
	res := api.SetupResult{ServerConf: conf, Warnings: toProblems(problems)}
	if paths.Group == "" {
		res.Warnings = append(res.Warnings, api.ConfigProblem{Message: "OpenVPN keeps running as root because the CRL is only in the PKI directory; set TW_CRL_COPIES=" +
			filepath.Join(dir, "crl.pem") + " and add user nobody / group nogroup on the Server page."})
	}
	wctx, cancel := context.WithTimeout(ctx, s.setupWait())
	defer cancel()
	if v, err := s.waitForOpenVPN(wctx, s.setupWait()); err == nil {
		s.conf.MarkRunning(conf)
		res.Running, res.OpenVPNVersion = true, v
		res.Message = "OpenVPN is running. Create the first client."
	} else {
		res.Message = "The configuration is written, but OpenVPN does not answer yet. Start it (or check its log); this page notices when it runs."
	}
	return res, http.StatusOK, nil
}

func (s *Server) setupWait() time.Duration {
	if s.restartWait > 0 {
		return s.restartWait
	}
	return 20 * time.Second
}

func keyType(name string) (pki.KeyType, error) {
	switch name {
	case "ec256":
		return pki.KeyType{Algo: "ec", Curve: elliptic.P256()}, nil
	case "ec384":
		return pki.KeyType{Algo: "ec", Curve: elliptic.P384()}, nil
	case "rsa2048", "rsa3072", "rsa4096":
		bits, _ := strconv.Atoi(strings.TrimPrefix(name, "rsa"))
		return pki.KeyType{Algo: "rsa", RSABits: bits}, nil
	}
	return pki.KeyType{}, fmt.Errorf("unknown key type %q", name)
}

// setupPaths makes the file references for server.conf, relative to the
// OpenVPN dir where possible (OpenVPN runs from it).
func (s *Server) setupPaths(req setup.Request) (setup.Paths, error) {
	dir := s.cfg.OpenVPNDir
	rel := func(p string) string {
		if r, err := filepath.Rel(dir, p); err == nil && !strings.HasPrefix(r, "..") {
			return r
		}
		return p
	}
	// OpenVPN re-reads the CRL at every connect, after dropping to nobody:
	// it must be a readable copy outside the private PKI directory.
	crl, group := filepath.Join(s.cfg.PKIDir, "crl.pem"), ""
	for _, c := range s.cfg.CRLCopies {
		if filepath.Dir(c) == filepath.Clean(dir) {
			crl, group = c, nobodyGroup()
			break
		}
	}
	host, port, err := net.SplitHostPort(s.cfg.MgmtAddr)
	if err != nil {
		return setup.Paths{}, fmt.Errorf("management address %q: %v", s.cfg.MgmtAddr, err)
	}
	bin, err := os.Executable()
	if err != nil {
		bin = "/usr/local/bin/tunnelward"
	}
	ccdDir, _ := filepath.Abs(s.cfg.CCDDir)
	p := setup.Paths{
		CA:         rel(filepath.Join(s.cfg.PKIDir, "ca.crt")),
		Cert:       rel(filepath.Join(s.cfg.PKIDir, "issued", serverCertName+".crt")),
		Key:        rel(filepath.Join(s.cfg.PKIDir, "private", serverCertName+".key")),
		CRL:        rel(crl),
		CCD:        rel(s.cfg.CCDDir),
		Management: host + " " + port,
		AuthVerify: bin + " auth-verify --ccd " + ccdDir,
		Group:      group,
	}
	switch req.TLS {
	case setup.TLSCryptV2:
		p.TLSKey = "tls-crypt-v2.key"
	case setup.TLSCrypt:
		p.TLSKey = "tls-crypt.key"
	case setup.TLSAuth:
		p.TLSKey = "ta.key"
	}
	return p, nil
}

// genTLSKey creates the control channel key with openvpn, unless one exists.
func (s *Server) genTLSKey(ctx context.Context, mode, path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	kind := "secret"
	if mode == setup.TLSCryptV2 {
		kind = "tls-crypt-v2-server"
	}
	bin := s.cfg.OpenVPNBin
	if bin == "" {
		bin = "openvpn"
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--genkey", kind, path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s --genkey %s: %v: %s", bin, kind, err, strings.TrimSpace(string(out)))
	}
	return os.Chmod(path, 0o600)
}

// nobodyGroup is the unprivileged group OpenVPN drops to: Debian and Alpine
// call it nogroup, Red Hat nobody.
func nobodyGroup() string {
	f, err := os.Open("/etc/group")
	if err != nil {
		return "nogroup"
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	nobody := false
	for sc.Scan() {
		switch strings.SplitN(sc.Text(), ":", 2)[0] {
		case "nogroup":
			return "nogroup"
		case "nobody":
			nobody = true
		}
	}
	if nobody {
		return "nobody"
	}
	return "nogroup"
}
