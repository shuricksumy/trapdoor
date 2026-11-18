package pki

import (
	"context"
	"crypto"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }
func newClock() *clock               { return &clock{t: time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)} }
func ctx() context.Context           { return context.Background() }
func fileExists(p string) bool       { _, err := os.Stat(p); return err == nil }
func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newTestStore(t *testing.T, opts Options) (*Store, *clock) {
	t.Helper()
	c := newClock()
	if opts.Now == nil {
		opts.Now = c.Now
	}
	s, err := Init(filepath.Join(t.TempDir(), "pki"), InitOptions{CommonName: "Test CA"}, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s, c
}

// verify checks that issued/<name>.crt chains to the CA for client auth.
func verify(t *testing.T, s *Store, name string, at time.Time) error {
	t.Helper()
	cert, err := s.readCert(s.path("issued", name+".crt"))
	if err != nil {
		return err
	}
	ca, err := s.readCert(s.path("ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	_, err = cert.Verify(x509.VerifyOptions{
		Roots:       roots,
		CurrentTime: at,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	return err
}

func crlSerials(t *testing.T, s *Store) map[string]int {
	t.Helper()
	block, _ := pem.Decode(mustRead(t, s.path("crl.pem")))
	if block == nil {
		t.Fatal("crl.pem: no PEM")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := s.readCert(s.path("ca.crt"))
	if err := crl.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("CRL signature: %v", err)
	}
	out := map[string]int{}
	for _, e := range crl.RevokedCertificateEntries {
		out[serialHex(e.SerialNumber)] = e.ReasonCode
	}
	return out
}

func TestInitLayout(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	for _, f := range []string{"ca.crt", "private/ca.key", "index.txt", "index.txt.attr", "serial", "crl.pem",
		"issued", "reqs", "certs_by_serial", "revoked/certs_by_serial"} {
		if !fileExists(s.path(f)) {
			t.Errorf("missing %s", f)
		}
	}
	if fi, _ := os.Stat(s.path("private/ca.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("ca.key mode = %v", fi.Mode().Perm())
	}
	if _, err := Init(s.Dir(), InitOptions{}, Options{}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("second Init: err = %v, want ErrAlreadyExists", err)
	}
	if _, err := Open(s.Dir(), Options{}); err != nil {
		t.Errorf("Open: %v", err)
	}
	if _, err := Open(t.TempDir(), Options{}); !errors.Is(err, ErrNotInitialized) {
		t.Errorf("Open empty dir: err = %v", err)
	}
}

func TestIssue(t *testing.T) {
	s, c := newTestStore(t, Options{})
	cert, err := s.Issue(ctx(), IssueRequest{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if cert.Status != StatusValid || !cert.Current || len(cert.Serial) != 32 {
		t.Fatalf("cert = %+v", cert)
	}
	if want := c.Now().Add(DefaultCertValidity); !cert.NotAfter.Equal(want) {
		t.Errorf("NotAfter = %v, want %v", cert.NotAfter, want)
	}
	if err := verify(t, s, "alice", c.Now()); err != nil {
		t.Errorf("verify: %v", err)
	}
	for _, f := range []string{"private/alice.key", "reqs/alice.req", "certs_by_serial/" + cert.Serial + ".pem"} {
		if !fileExists(s.path(f)) {
			t.Errorf("missing %s", f)
		}
	}
	if fi, _ := os.Stat(s.path("private/alice.key")); fi.Mode().Perm() != 0o600 {
		t.Errorf("key mode = %v", fi.Mode().Perm())
	}
	if _, err := s.Issue(ctx(), IssueRequest{Name: "alice"}); !errors.Is(err, ErrAlreadyExists) {
		t.Errorf("duplicate: err = %v", err)
	}
	for _, bad := range []string{"../etc", "a b", ""} {
		if _, err := s.Issue(ctx(), IssueRequest{Name: bad}); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Issue(%q): err = %v", bad, err)
		}
	}
}

func TestIssueServer(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	if _, err := s.Issue(ctx(), IssueRequest{Name: "server", Kind: KindServer}); err != nil {
		t.Fatal(err)
	}
	cert, err := s.readCert(s.path("issued/server.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Errorf("ExtKeyUsage = %v", cert.ExtKeyUsage)
	}
}

func TestIssueWithPassphrase(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	if _, err := s.Issue(ctx(), IssueRequest{Name: "bob", Passphrase: "s3cret pass"}); err != nil {
		t.Fatal(err)
	}
	data := mustRead(t, s.path("private/bob.key"))
	if block, _ := pem.Decode(data); block == nil || block.Type != "ENCRYPTED PRIVATE KEY" {
		t.Fatalf("key is not encrypted PKCS#8")
	}
	if _, err := decodeKey(data, ""); !errors.Is(err, ErrKeyEncrypted) {
		t.Errorf("no passphrase: err = %v", err)
	}
	if _, err := decodeKey(data, "wrong"); !errors.Is(err, ErrBadPassphrase) {
		t.Errorf("wrong passphrase: err = %v", err)
	}
	key, err := decodeKey(data, "s3cret pass")
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := s.readCert(s.path("issued/bob.crt"))
	if !samePub(key.Public(), cert.PublicKey) {
		t.Error("decrypted key does not match certificate")
	}
}

func TestRevokeUnrevoke(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	cert, err := s.Issue(ctx(), IssueRequest{Name: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	sn := cert.Serial

	if err := s.Revoke(ctx(), "alice"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(ctx(), "alice")
	if got.Status != StatusRevoked || got.RevokedAt.IsZero() {
		t.Fatalf("after revoke: %+v", got)
	}
	for _, f := range []string{"issued/alice.crt", "private/alice.key", "reqs/alice.req", "certs_by_serial/" + sn + ".pem"} {
		if fileExists(s.path(f)) {
			t.Errorf("%s should have moved", f)
		}
	}
	for _, f := range []string{"revoked/certs_by_serial/" + sn + ".crt", "revoked/private_by_serial/" + sn + ".key", "revoked/reqs_by_serial/" + sn + ".req"} {
		if !fileExists(s.path(f)) {
			t.Errorf("missing %s", f)
		}
	}
	if _, ok := crlSerials(t, s)[sn]; !ok {
		t.Error("serial not in CRL after revoke")
	}
	if err := s.Revoke(ctx(), "alice"); !errors.Is(err, ErrNotValid) {
		t.Errorf("double revoke: err = %v", err)
	}

	if err := s.Unrevoke(ctx(), "alice"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get(ctx(), "alice")
	if got.Status != StatusValid || !got.RevokedAt.IsZero() || got.Serial != sn {
		t.Fatalf("after unrevoke: %+v", got)
	}
	for _, f := range []string{"issued/alice.crt", "private/alice.key", "reqs/alice.req", "certs_by_serial/" + sn + ".pem"} {
		if !fileExists(s.path(f)) {
			t.Errorf("missing %s after unrevoke", f)
		}
	}
	if _, ok := crlSerials(t, s)[sn]; ok {
		t.Error("serial still in CRL after unrevoke")
	}
	if err := s.Unrevoke(ctx(), "alice"); !errors.Is(err, ErrNotRevoked) {
		t.Errorf("unrevoke valid: err = %v", err)
	}
	if err := s.Revoke(ctx(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoke unknown: err = %v", err)
	}
}

func TestRenewExpired(t *testing.T) {
	s, c := newTestStore(t, Options{})
	old, err := s.Issue(ctx(), IssueRequest{Name: "alice", Validity: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	oldCert, _ := s.readCert(s.path("issued/alice.crt"))

	c.Add(2 * time.Hour)
	if got, _ := s.Get(ctx(), "alice"); got.Status != StatusExpired {
		t.Fatalf("status = %s, want expired", got.Status)
	}
	if verify(t, s, "alice", c.Now()) == nil {
		t.Fatal("expired cert verified")
	}

	renewed, err := s.Renew(ctx(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.Serial == old.Serial || renewed.Status != StatusValid {
		t.Fatalf("renewed = %+v", renewed)
	}
	if err := verify(t, s, "alice", c.Now()); err != nil {
		t.Errorf("verify renewed: %v", err)
	}
	newCert, _ := s.readCert(s.path("issued/alice.crt"))
	if !samePub(newCert.PublicKey, oldCert.PublicKey) {
		t.Error("renew should keep the key")
	}

	list, _ := s.List(ctx())
	if len(list) != 2 || list[0].Status != StatusRevoked || list[0].RevokeReason != "superseded" || list[0].Current || !list[1].Current {
		t.Fatalf("list = %+v", list)
	}
	if reason := crlSerials(t, s)[old.Serial]; reason != 4 {
		t.Errorf("old serial CRL reason = %d, want 4 (superseded)", reason)
	}
	if !fileExists(s.path("revoked/certs_by_serial/" + old.Serial + ".crt")) {
		t.Error("old cert not archived")
	}

	if err := s.Revoke(ctx(), "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Renew(ctx(), "alice", 0); !errors.Is(err, ErrNotValid) {
		t.Errorf("renew revoked: err = %v", err)
	}
}

func TestRemove(t *testing.T) {
	s, c := newTestStore(t, Options{})
	if _, err := s.Issue(ctx(), IssueRequest{Name: "keep"}); err != nil {
		t.Fatal(err)
	}
	old, _ := s.Issue(ctx(), IssueRequest{Name: "bob", Validity: time.Hour})
	c.Add(2 * time.Hour)
	cur, err := s.Renew(ctx(), "bob", 0) // old entry: revoked, expired
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Remove(ctx(), "bob"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"issued/bob.crt", "private/bob.key", "reqs/bob.req",
		"revoked/certs_by_serial/" + old.Serial + ".crt", "revoked/certs_by_serial/" + cur.Serial + ".crt",
		"revoked/private_by_serial/" + cur.Serial + ".key"} {
		if fileExists(s.path(f)) {
			t.Errorf("%s should be deleted", f)
		}
	}
	list, _ := s.List(ctx())
	if len(list) != 2 || list[1].Name != "bob" || list[1].Serial != cur.Serial || list[1].Status != StatusRevoked {
		t.Fatalf("list = %+v (expired entry should be dropped, current kept as revoked)", list)
	}
	if _, ok := crlSerials(t, s)[cur.Serial]; !ok {
		t.Error("removed cert must stay in CRL")
	}
	if _, err := s.Issue(ctx(), IssueRequest{Name: "bob"}); err != nil {
		t.Errorf("re-issue after remove: %v", err)
	}
	if err := verify(t, s, "keep", c.Now()); err != nil {
		t.Errorf("other certs untouched: %v", err)
	}
	if err := s.Remove(ctx(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove unknown: err = %v", err)
	}
}

func TestCRLCopiesAndNumber(t *testing.T) {
	copyPath := filepath.Join(t.TempDir(), "crl.pem")
	s, c := newTestStore(t, Options{CRLCopies: []string{copyPath}})
	first := mustRead(t, copyPath)

	num := func() int64 {
		block, _ := pem.Decode(mustRead(t, s.path("crl.pem")))
		crl, err := x509.ParseRevocationList(block.Bytes)
		if err != nil {
			t.Fatal(err)
		}
		return crl.Number.Int64()
	}
	n1 := num()
	if err := s.GenCRL(ctx()); err != nil { // same second: must still increase
		t.Fatal(err)
	}
	if n2 := num(); n2 <= n1 {
		t.Errorf("CRL number did not increase: %d → %d", n1, n2)
	}
	c.Add(time.Minute)
	if err := s.GenCRL(ctx()); err != nil {
		t.Fatal(err)
	}
	if string(mustRead(t, copyPath)) == string(first) {
		t.Error("CRL copy not updated")
	}
}

func TestConcurrentIssue(t *testing.T) {
	s, _ := newTestStore(t, Options{})
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Issue(ctx(), IssueRequest{Name: fmt.Sprintf("user%d", i)})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	list, err := s.List(ctx())
	if err != nil || len(list) != 20 {
		t.Fatalf("list: %d entries, err %v", len(list), err)
	}
}

func TestRSACA(t *testing.T) {
	s, err := Init(filepath.Join(t.TempDir(), "pki"), InitOptions{KeyType: KeyType{Algo: "rsa", RSABits: 2048}}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(ctx(), IssueRequest{Name: "rsauser", Passphrase: "pw"}); err != nil {
		t.Fatal(err)
	}
	if err := verify(t, s, "rsauser", time.Now()); err != nil {
		t.Error(err)
	}
}

func samePub(a, b crypto.PublicKey) bool {
	k, ok := a.(interface{ Equal(crypto.PublicKey) bool })
	return ok && k.Equal(b)
}

func TestBundle(t *testing.T) {
	s, c := newTestStore(t, Options{})
	if _, err := s.Issue(ctx(), IssueRequest{Name: "alice", Validity: time.Hour}); err != nil {
		t.Fatal(err)
	}
	b, err := s.Bundle(ctx(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	if block, rest := pem.Decode(b.Cert); block == nil || block.Type != "CERTIFICATE" || len(rest) != 0 {
		t.Error("Cert must be a single clean PEM block")
	}
	if _, err := decodeKey(b.Key, ""); err != nil {
		t.Errorf("key: %v", err)
	}
	if _, err := decodeCert(b.CA); err != nil {
		t.Errorf("CA: %v", err)
	}
	c.Add(2 * time.Hour)
	if _, err := s.Bundle(ctx(), "alice"); !errors.Is(err, ErrNotValid) {
		t.Errorf("expired: err = %v, want ErrNotValid", err)
	}
	if _, err := s.Bundle(ctx(), "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
}
