package pki

// Compatibility tests against the real easyrsa and openssl CLIs.
// They run only when TW_EASYRSA_DIR points at an unpacked EasyRSA 3.x release
// and openssl is on PATH (CI does both; see .github/workflows/ci.yml).

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type easyrsa struct {
	t   *testing.T
	dir string // easyrsa installation
	pki string
}

func newEasyRSA(t *testing.T) *easyrsa {
	t.Helper()
	dir := os.Getenv("TW_EASYRSA_DIR")
	if dir == "" {
		t.Skip("TW_EASYRSA_DIR not set")
	}
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("openssl not found")
	}
	return &easyrsa{t: t, dir: dir, pki: filepath.Join(t.TempDir(), "pki")}
}

func (e *easyrsa) run(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(filepath.Join(e.dir, "easyrsa"), append([]string{"--batch"}, args...)...)
	cmd.Dir = e.dir
	cmd.Env = append(os.Environ(),
		"EASYRSA="+e.dir, "EASYRSA_PKI="+e.pki,
		"EASYRSA_ALGO=ec", "EASYRSA_CURVE=prime256v1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("easyrsa %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func openssl(t *testing.T, args ...string) (string, error) {
	t.Helper()
	out, err := exec.Command("openssl", args...).CombinedOutput()
	return string(out), err
}

func mustOpenSSL(t *testing.T, args ...string) string {
	t.Helper()
	out, err := openssl(t, args...)
	if err != nil {
		t.Fatalf("openssl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// verifyWithCRL runs `openssl verify` with CRL checking and reports success.
func verifyWithCRL(t *testing.T, pkiDir, certPath string) (bool, string) {
	t.Helper()
	out, err := openssl(t, "verify", "-crl_check", "-purpose", "sslclient",
		"-CAfile", filepath.Join(pkiDir, "ca.crt"),
		"-CRLfile", filepath.Join(pkiDir, "crl.pem"), certPath)
	return err == nil, out
}

func statusOf(t *testing.T, s *Store, name string) Status {
	t.Helper()
	c, err := s.Get(ctx(), name)
	if err != nil {
		t.Fatalf("Get(%s): %v", name, err)
	}
	return c.Status
}

// An easyrsa-created PKI is managed by tunnelward, and easyrsa still works on it.
func TestCompatEasyRSAToTunnelward(t *testing.T) {
	e := newEasyRSA(t)
	e.run("init-pki")
	e.run("--req-cn=Compat CA", "build-ca", "nopass")
	e.run("build-client-full", "alice", "nopass")
	e.run("--passout=pass:bobpass", "build-client-full", "bob")
	e.run("revoke", "bob")
	e.run("gen-crl")

	s, err := Open(e.pki, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(t, s, "alice"); st != StatusValid {
		t.Errorf("alice = %s", st)
	}
	if st := statusOf(t, s, "bob"); st != StatusRevoked {
		t.Errorf("bob = %s", st)
	}

	// Issue with passphrase: openssl must verify cert and decrypt the key.
	if _, err := s.Issue(ctx(), IssueRequest{Name: "carol", Passphrase: "carol pw"}); err != nil {
		t.Fatal(err)
	}
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "issued/carol.crt")); !ok {
		t.Errorf("openssl verify carol: %s", out)
	}
	mustOpenSSL(t, "pkey", "-in", filepath.Join(e.pki, "private/carol.key"), "-passin", "pass:carol pw", "-noout")
	if _, err := openssl(t, "pkey", "-in", filepath.Join(e.pki, "private/carol.key"), "-passin", "pass:wrong", "-noout"); err == nil {
		t.Error("openssl accepted wrong passphrase")
	}

	// Revoke an easyrsa-issued cert: openssl must see it in our CRL.
	alice, _ := s.Get(ctx(), "alice")
	if err := s.Revoke(ctx(), "alice"); err != nil {
		t.Fatal(err)
	}
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "revoked/certs_by_serial", alice.Serial+".crt")); ok || !strings.Contains(out, "revoked") {
		t.Errorf("revoked alice still verifies: %s", out)
	}
	if err := s.Unrevoke(ctx(), "alice"); err != nil {
		t.Fatal(err)
	}
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "issued/alice.crt")); !ok {
		t.Errorf("unrevoked alice: %s", out)
	}

	// Renew: new cert verifies, old one is in the CRL.
	oldCarol, _ := s.Get(ctx(), "carol")
	if _, err := s.Renew(ctx(), "carol", 0); err != nil {
		t.Fatal(err)
	}
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "issued/carol.crt")); !ok {
		t.Errorf("renewed carol: %s", out)
	}
	if ok, _ := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "revoked/certs_by_serial", oldCarol.Serial+".crt")); ok {
		t.Error("superseded carol cert still verifies")
	}

	// easyrsa keeps working on the PKI tunnelward wrote.
	e.run("build-client-full", "dave", "nopass")
	e.run("show-cert", "carol")
	e.run("revoke", "carol")
	e.run("gen-crl")
	mustOpenSSL(t, "crl", "-in", filepath.Join(e.pki, "crl.pem"), "-noout")

	if st := statusOf(t, s, "dave"); st != StatusValid {
		t.Errorf("dave = %s", st)
	}
	if st := statusOf(t, s, "carol"); st != StatusRevoked {
		t.Errorf("carol after easyrsa revoke = %s", st)
	}
	if err := s.GenCRL(ctx()); err != nil {
		t.Fatal(err)
	}
}

// A tunnelward-created PKI can be used by easyrsa.
func TestCompatTunnelwardToEasyRSA(t *testing.T) {
	e := newEasyRSA(t)
	s, err := Init(e.pki, InitOptions{CommonName: "TW CA"}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustOpenSSL(t, "crl", "-in", filepath.Join(e.pki, "crl.pem"), "-noout")

	e.run("build-client-full", "eve", "nopass")
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "issued/eve.crt")); !ok {
		t.Errorf("easyrsa-issued eve: %s", out)
	}
	if _, err := s.Issue(ctx(), IssueRequest{Name: "frank"}); err != nil {
		t.Fatal(err)
	}
	e.run("revoke", "frank") // easyrsa revokes a cert tunnelward issued
	e.run("gen-crl")

	if st := statusOf(t, s, "frank"); st != StatusRevoked {
		t.Errorf("frank = %s", st)
	}
	if st := statusOf(t, s, "eve"); st != StatusValid {
		t.Errorf("eve = %s", st)
	}
	frank, _ := s.Get(ctx(), "frank")
	if ok, _ := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "revoked/certs_by_serial", frank.Serial+".crt")); ok {
		t.Error("frank still verifies after easyrsa revoke")
	}
	if err := s.Unrevoke(ctx(), "frank"); err != nil {
		t.Fatalf("unrevoke easyrsa-revoked cert: %v", err)
	}
	if ok, out := verifyWithCRL(t, e.pki, filepath.Join(e.pki, "issued/frank.crt")); !ok {
		t.Errorf("frank after unrevoke: %s", out)
	}
}
