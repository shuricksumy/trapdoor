package pki

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Defaults match easyrsa (certs) and the old openvpn-ui setup (CRL).
const (
	DefaultCertValidity = 825 * 24 * time.Hour
	DefaultCAValidity   = 3650 * 24 * time.Hour
	// An expired CRL makes OpenVPN reject every client, so it is long-lived.
	DefaultCRLValidity = 3650 * 24 * time.Hour
)

// Options configure a Store.
type Options struct {
	CertValidity time.Duration // default DefaultCertValidity
	CRLValidity  time.Duration // default DefaultCRLValidity
	// CAPassphrase unlocks an encrypted ca.key.
	CAPassphrase string
	// CRLCopies are extra paths that get a copy of crl.pem after every
	// change, e.g. /etc/openvpn/crl.pem referenced by server.conf.
	CRLCopies []string
	// Now is the clock; nil means time.Now. Used by tests.
	Now func() time.Time
}

// InitOptions configure a new certificate authority.
type InitOptions struct {
	CommonName string        // CA common name, default "tunnelward CA"
	KeyType    KeyType       // default DefaultKeyType
	Validity   time.Duration // default DefaultCAValidity
}

// Store is the native Go PKI on an easyrsa-compatible directory.
// It is safe for concurrent use.
type Store struct {
	dir  string
	opts Options
	mu   sync.Mutex
}

var _ PKI = (*Store)(nil)

// Open opens an existing PKI directory (the easyrsa "pki" dir).
func Open(dir string, opts Options) (*Store, error) {
	s := newStore(dir, opts)
	for _, f := range []string{"ca.crt", "private/ca.key", "index.txt"} {
		if _, err := os.Stat(s.path(f)); err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrNotInitialized, dir, err)
		}
	}
	if _, _, err := s.loadCA(); err != nil {
		return nil, err
	}
	return s, nil
}

// Init creates a new PKI with a self-signed CA in dir and returns it.
func Init(dir string, init InitOptions, opts Options) (*Store, error) {
	s := newStore(dir, opts)
	if _, err := os.Stat(s.path("ca.crt")); err == nil {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, s.path("ca.crt"))
	}
	if init.CommonName == "" {
		init.CommonName = "tunnelward CA"
	}
	if init.KeyType.Algo == "" {
		init.KeyType = DefaultKeyType
	}
	if init.Validity == 0 {
		init.Validity = DefaultCAValidity
	}

	for _, d := range []string{
		"private", "reqs", "issued", "certs_by_serial", "renewed/issued",
		"revoked/certs_by_serial", "revoked/private_by_serial", "revoked/reqs_by_serial",
	} {
		if err := os.MkdirAll(s.path(d), 0o700); err != nil {
			return nil, err
		}
	}

	key, err := generateKey(init.KeyType)
	if err != nil {
		return nil, err
	}
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}
	ski, err := subjectKeyID(key.Public())
	if err != nil {
		return nil, err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: init.CommonName},
		NotBefore:             now,
		NotAfter:              now.Add(init.Validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          ski,
		SignatureAlgorithm:    signatureAlgorithm(key.Public()),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}
	keyPEM, err := encodeKey(key, opts.CAPassphrase)
	if err != nil {
		return nil, err
	}
	next, err := newSerial()
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		data []byte
		perm fs.FileMode
	}{
		{"private/ca.key", keyPEM, 0o600},
		{"ca.crt", encodeCert(der), 0o644},
		{"index.txt", nil, 0o644},
		{"index.txt.attr", []byte("unique_subject = no\n"), 0o644},
		{"serial", []byte(serialHex(next) + "\n"), 0o644},
	} {
		if err := writeFileAtomic(s.path(f.name), f.data, f.perm); err != nil {
			return nil, err
		}
	}
	if err := s.GenCRL(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func newStore(dir string, opts Options) *Store {
	if opts.CertValidity == 0 {
		opts.CertValidity = DefaultCertValidity
	}
	if opts.CRLValidity == 0 {
		opts.CRLValidity = DefaultCRLValidity
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Store{dir: dir, opts: opts}
}

// Dir returns the PKI directory.
func (s *Store) Dir() string { return s.dir }

// CACert returns the CA certificate in PEM form.
func (s *Store) CACert() ([]byte, error) { return os.ReadFile(s.path("ca.crt")) }

// List implements PKI.
func (s *Store) List(_ context.Context) ([]Cert, error) {
	var out []Cert
	err := s.withLock(false, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		now := s.now()
		last := map[string]int{}
		for i, e := range entries {
			last[e.CN()] = i
		}
		for i, e := range entries {
			out = append(out, toCert(e, now, last[e.CN()] == i))
		}
		return nil
	})
	return out, err
}

// Get implements PKI.
func (s *Store) Get(_ context.Context, name string) (Cert, error) {
	var out Cert
	err := s.withLock(false, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		i := current(entries, name)
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		out = toCert(entries[i], s.now(), true)
		return nil
	})
	return out, err
}

// Issue implements PKI.
func (s *Store) Issue(_ context.Context, req IssueRequest) (Cert, error) {
	if !ValidName(req.Name) {
		return Cert{}, fmt.Errorf("%w: %q", ErrInvalidName, req.Name)
	}
	if req.Kind == "" {
		req.Kind = KindClient
	}
	if req.Kind != KindClient && req.Kind != KindServer {
		return Cert{}, fmt.Errorf("%w: %q", ErrInvalidKind, req.Kind)
	}
	name := req.Name

	var out Cert
	err := s.withLock(true, func() error {
		caCert, caKey, err := s.loadCA()
		if err != nil {
			return err
		}
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		now := s.now()
		if i := current(entries, name); i >= 0 && entries[i].status(now) == StatusValid {
			return fmt.Errorf("%w: %s has a valid certificate", ErrAlreadyExists, name)
		}
		for _, f := range []string{"issued/" + name + ".crt", "private/" + name + ".key"} {
			if exists(s.path(f)) {
				return fmt.Errorf("%w: %s exists (renew or remove it first)", ErrAlreadyExists, f)
			}
		}

		kt, err := keyTypeOf(caCert.PublicKey)
		if err != nil {
			return err
		}
		key, err := generateKey(kt)
		if err != nil {
			return err
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
			Subject:            pkix.Name{CommonName: name},
			SignatureAlgorithm: signatureAlgorithm(key.Public()),
		}, key)
		if err != nil {
			return err
		}
		keyPEM, err := encodeKey(key, req.Passphrase)
		if err != nil {
			return err
		}
		cert, certPEM, err := s.sign(caCert, caKey, name, req.Kind, key.Public(), req.Validity)
		if err != nil {
			return err
		}
		serial := serialHex(cert.SerialNumber)

		if err := writeFileAtomic(s.path("private", name+".key"), keyPEM, 0o600); err != nil {
			return err
		}
		reqPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
		if err := writeFileAtomic(s.path("reqs", name+".req"), reqPEM, 0o644); err != nil {
			return err
		}
		if err := s.writeIssued(name, serial, certPEM); err != nil {
			return err
		}

		e := indexEntry{Status: 'V', NotAfter: cert.NotAfter, Serial: serial, File: "unknown", Subject: "/CN=" + name}
		entries = append(entries, e)
		if err := s.writeIndex(entries); err != nil {
			return err
		}
		if err := s.writeNextSerial(cert.SerialNumber); err != nil {
			return err
		}
		out = toCert(e, now, true)
		return nil
	})
	return out, err
}

// Revoke implements PKI.
func (s *Store) Revoke(_ context.Context, name string) error {
	return s.withLock(true, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		i := current(entries, name)
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		if entries[i].Status == 'R' {
			return fmt.Errorf("%w: %s is already revoked", ErrNotValid, name)
		}
		s.markRevoked(&entries[i], "")
		if err := s.moveToRevoked(name, entries[i].Serial, true); err != nil {
			return err
		}
		if err := s.writeIndex(entries); err != nil {
			return err
		}
		return s.genCRL(entries)
	})
}

// Unrevoke implements PKI. It restores the files easyrsa moved on revoke.
func (s *Store) Unrevoke(_ context.Context, name string) error {
	return s.withLock(true, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		i := current(entries, name)
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		e := &entries[i]
		if e.Status != 'R' {
			return fmt.Errorf("%w: %s", ErrNotRevoked, name)
		}
		if exists(s.path("issued", name+".crt")) {
			return fmt.Errorf("%w: issued/%s.crt", ErrAlreadyExists, name)
		}
		revCert := s.path("revoked/certs_by_serial", e.Serial+".crt")
		data, err := os.ReadFile(revCert)
		if err != nil {
			return fmt.Errorf("cannot unrevoke %s: certificate file is gone (was it removed?): %w", name, err)
		}
		if err := s.writeIssued(name, e.Serial, data); err != nil {
			return err
		}
		if err := os.Remove(revCert); err != nil {
			return err
		}
		if err := moveIfExists(s.path("revoked/private_by_serial", e.Serial+".key"), s.path("private", name+".key")); err != nil {
			return err
		}
		if err := moveIfExists(s.path("revoked/reqs_by_serial", e.Serial+".req"), s.path("reqs", name+".req")); err != nil {
			return err
		}
		e.Status, e.RevokedAt, e.Reason = 'V', time.Time{}, ""
		if err := s.writeIndex(entries); err != nil {
			return err
		}
		return s.genCRL(entries)
	})
}

// Renew implements PKI.
func (s *Store) Renew(_ context.Context, name string, validity time.Duration) (Cert, error) {
	var out Cert
	err := s.withLock(true, func() error {
		caCert, caKey, err := s.loadCA()
		if err != nil {
			return err
		}
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		i := current(entries, name)
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		if entries[i].Status == 'R' {
			return fmt.Errorf("%w: %s is revoked; unrevoke it or issue a new one", ErrNotValid, name)
		}
		old, err := s.readCert(s.path("issued", name+".crt"))
		if err != nil {
			return err
		}
		if serialHex(old.SerialNumber) != entries[i].Serial {
			return fmt.Errorf("issued/%s.crt does not match index.txt serial %s", name, entries[i].Serial)
		}
		kind := KindClient
		if slices.Contains(old.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
			kind = KindServer
		}

		cert, certPEM, err := s.sign(caCert, caKey, name, kind, old.PublicKey, validity)
		if err != nil {
			return err
		}
		// Retire the old certificate, keep key and request for the new one.
		s.markRevoked(&entries[i], "superseded")
		if err := s.moveToRevoked(name, entries[i].Serial, false); err != nil {
			return err
		}
		serial := serialHex(cert.SerialNumber)
		if err := s.writeIssued(name, serial, certPEM); err != nil {
			return err
		}
		e := indexEntry{Status: 'V', NotAfter: cert.NotAfter, Serial: serial, File: "unknown", Subject: "/CN=" + name}
		entries = append(entries, e)
		if err := s.writeIndex(entries); err != nil {
			return err
		}
		if err := s.writeNextSerial(cert.SerialNumber); err != nil {
			return err
		}
		out = toCert(e, s.now(), true)
		return s.genCRL(entries)
	})
	return out, err
}

// Remove implements PKI.
func (s *Store) Remove(_ context.Context, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return s.withLock(true, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		now := s.now()
		var serials []string
		kept := entries[:0:0]
		for _, e := range entries {
			if e.CN() != name {
				kept = append(kept, e)
				continue
			}
			serials = append(serials, e.Serial)
			if !now.Before(e.NotAfter) {
				continue // expired: no longer needed in the CRL
			}
			if e.Status != 'R' {
				s.markRevoked(&e, "cessationOfOperation")
			}
			kept = append(kept, e) // stays in the CRL until it expires
		}
		if len(serials) == 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}

		files := []string{
			"issued/" + name + ".crt", "private/" + name + ".key", "reqs/" + name + ".req",
			"renewed/issued/" + name + ".crt", "inline/private/" + name + ".inline", "inline/" + name + ".inline",
		}
		for _, sn := range serials {
			files = append(files,
				"certs_by_serial/"+sn+".pem",
				"revoked/certs_by_serial/"+sn+".crt",
				"revoked/private_by_serial/"+sn+".key",
				"revoked/reqs_by_serial/"+sn+".req")
		}
		for _, f := range files {
			if err := os.Remove(s.path(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if err := s.writeIndex(kept); err != nil {
			return err
		}
		return s.genCRL(kept)
	})
}

// GenCRL implements PKI.
func (s *Store) GenCRL(_ context.Context) error {
	return s.withLock(true, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		return s.genCRL(entries)
	})
}

// Bundle implements PKI.
func (s *Store) Bundle(_ context.Context, name string) (Bundle, error) {
	var out Bundle
	err := s.withLock(false, func() error {
		entries, err := s.readIndex()
		if err != nil {
			return err
		}
		i := current(entries, name)
		if i < 0 {
			return fmt.Errorf("%w: %s", ErrNotFound, name)
		}
		if st := entries[i].status(s.now()); st != StatusValid {
			return fmt.Errorf("%w: %s is %s", ErrNotValid, name, st)
		}
		cert, err := s.readCert(s.path("issued", name+".crt"))
		if err != nil {
			return err
		}
		if serialHex(cert.SerialNumber) != entries[i].Serial {
			return fmt.Errorf("issued/%s.crt does not match index.txt serial %s", name, entries[i].Serial)
		}
		key, err := os.ReadFile(s.path("private", name+".key"))
		if err != nil {
			return fmt.Errorf("read key for %s: %w", name, err)
		}
		ca, err := os.ReadFile(s.path("ca.crt"))
		if err != nil {
			return err
		}
		out = Bundle{CA: ca, Cert: encodeCert(cert.Raw), Key: key}
		return nil
	})
	return out, err
}

// ---- internals (callers hold the lock) ----

func (s *Store) genCRL(entries []indexEntry) error {
	caCert, caKey, err := s.loadCA()
	if err != nil {
		return err
	}
	var revoked []x509.RevocationListEntry
	for _, e := range entries {
		if e.Status != 'R' {
			continue
		}
		sn, err := parseSerial(e.Serial)
		if err != nil {
			return err
		}
		revoked = append(revoked, x509.RevocationListEntry{
			SerialNumber:   sn,
			RevocationTime: e.RevokedAt,
			ReasonCode:     reasonCode(e.Reason),
		})
	}

	now := s.now()
	number := big.NewInt(now.Unix())
	// The CRL number must grow; use the clock, or previous+1 if that is higher.
	if data, err := os.ReadFile(s.path("crl.pem")); err == nil {
		if block, _ := pem.Decode(data); block != nil {
			if old, err := x509.ParseRevocationList(block.Bytes); err == nil && old.Number != nil && old.Number.Cmp(number) >= 0 {
				number = new(big.Int).Add(old.Number, big.NewInt(1))
			}
		}
	}

	issuer := caCert
	if len(issuer.SubjectKeyId) == 0 {
		// Go needs the issuer SKI for the CRL's authority key identifier.
		c := *caCert
		if c.SubjectKeyId, err = subjectKeyID(caCert.PublicKey); err != nil {
			return err
		}
		issuer = &c
	}
	der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{
		Number:                    number,
		ThisUpdate:                now,
		NextUpdate:                now.Add(s.opts.CRLValidity),
		RevokedCertificateEntries: revoked,
		SignatureAlgorithm:        signatureAlgorithm(caCert.PublicKey),
	}, issuer, caKey)
	if err != nil {
		return fmt.Errorf("create CRL: %w", err)
	}
	crlPEM := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: der})
	if err := writeFileAtomic(s.path("crl.pem"), crlPEM, 0o644); err != nil {
		return err
	}
	if err := writeFileAtomic(s.path("crl.der"), der, 0o644); err != nil {
		return err
	}
	for _, dst := range s.opts.CRLCopies {
		if err := writeFileAtomic(dst, crlPEM, 0o644); err != nil {
			return fmt.Errorf("copy CRL: %w", err)
		}
	}
	return nil
}

func (s *Store) sign(caCert *x509.Certificate, caKey crypto.Signer, name string, kind Kind, pub crypto.PublicKey, validity time.Duration) (*x509.Certificate, []byte, error) {
	if validity == 0 {
		validity = s.opts.CertValidity
	}
	serial, err := newSerial()
	if err != nil {
		return nil, nil, err
	}
	ski, err := subjectKeyID(pub)
	if err != nil {
		return nil, nil, err
	}
	now := s.now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now,
		NotAfter:              now.Add(validity),
		BasicConstraintsValid: true,
		SubjectKeyId:          ski,
		SignatureAlgorithm:    signatureAlgorithm(caCert.PublicKey),
	}
	switch kind {
	case KindServer: // easyrsa x509-types/server
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.DNSNames = []string{name}
	default: // easyrsa x509-types/client
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, pub, caKey)
	if err != nil {
		return nil, nil, fmt.Errorf("sign %s: %w", name, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, err
	}
	return cert, encodeCert(der), nil
}

// markRevoked sets e to revoked now with an optional openssl reason string.
func (s *Store) markRevoked(e *indexEntry, reason string) {
	e.Status = 'R'
	e.RevokedAt = s.now().Truncate(time.Second)
	e.Reason = reason
}

// moveToRevoked moves the certificate with serial to revoked/certs_by_serial.
// With withKey it also moves the private key and request, like easyrsa revoke.
func (s *Store) moveToRevoked(name, serial string, withKey bool) error {
	// The cert file may already be gone; index and CRL are still updated.
	dst := s.path("revoked/certs_by_serial", serial+".crt")
	for _, src := range []string{s.path("issued", name+".crt"), s.path("renewed/issued", name+".crt")} {
		c, err := s.readCert(src)
		if err != nil {
			continue
		}
		if serialHex(c.SerialNumber) == serial {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
			break
		}
	}
	if err := os.Remove(s.path("certs_by_serial", serial+".pem")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if !withKey {
		return nil
	}
	if err := moveIfExists(s.path("private", name+".key"), s.path("revoked/private_by_serial", serial+".key")); err != nil {
		return err
	}
	if err := moveIfExists(s.path("reqs", name+".req"), s.path("revoked/reqs_by_serial", serial+".req")); err != nil {
		return err
	}
	for _, f := range []string{"inline/private/" + name + ".inline", "inline/" + name + ".inline"} {
		if err := os.Remove(s.path(f)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) writeIssued(name, serial string, certPEM []byte) error {
	if err := writeFileAtomic(s.path("issued", name+".crt"), certPEM, 0o644); err != nil {
		return err
	}
	return writeFileAtomic(s.path("certs_by_serial", serial+".pem"), certPEM, 0o644)
}

// writeNextSerial keeps the openssl "serial" file pointing past the last
// serial used, so `easyrsa` keeps working on this PKI.
func (s *Store) writeNextSerial(last *big.Int) error {
	next := new(big.Int).Add(last, big.NewInt(1))
	return writeFileAtomic(s.path("serial"), []byte(serialHex(next)+"\n"), 0o644)
}

func (s *Store) loadCA() (*x509.Certificate, crypto.Signer, error) {
	cert, err := s.readCert(s.path("ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA certificate: %w", err)
	}
	data, err := os.ReadFile(s.path("private", "ca.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("read CA key: %w", err)
	}
	key, err := decodeKey(data, s.opts.CAPassphrase)
	if err != nil {
		return nil, nil, fmt.Errorf("CA key: %w", err)
	}
	return cert, key, nil
}

func (s *Store) readCert(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decodeCert(data)
}

func (s *Store) readIndex() ([]indexEntry, error) {
	data, err := os.ReadFile(s.path("index.txt"))
	if err != nil {
		return nil, err
	}
	return parseIndex(string(data))
}

func (s *Store) writeIndex(entries []indexEntry) error {
	if old, err := os.ReadFile(s.path("index.txt")); err == nil {
		if err := writeFileAtomic(s.path("index.txt.old"), old, 0o644); err != nil {
			return err
		}
	}
	return writeFileAtomic(s.path("index.txt"), []byte(formatIndex(entries)), 0o644)
}

func (s *Store) withLock(exclusive bool, fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, err := lockFile(s.dir, exclusive)
	if err != nil {
		return err
	}
	defer l.unlock()
	return fn()
}

func (s *Store) now() time.Time { return s.opts.Now().UTC().Truncate(time.Second) }

func (s *Store) path(parts ...string) string {
	return filepath.Join(append([]string{s.dir}, parts...)...)
}

// current returns the index of the newest entry for name, or -1.
func current(entries []indexEntry, name string) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].CN() == name {
			return i
		}
	}
	return -1
}

func toCert(e indexEntry, now time.Time, isCurrent bool) Cert {
	return Cert{
		Name:         e.CN(),
		Serial:       e.Serial,
		Status:       e.status(now),
		NotAfter:     e.NotAfter,
		RevokedAt:    e.RevokedAt,
		RevokeReason: e.Reason,
		Current:      isCurrent,
	}
}

// reasonCode maps openssl's CRL reason names to RFC 5280 codes.
func reasonCode(reason string) int {
	codes := map[string]int{
		"unspecified": 0, "keycompromise": 1, "cacompromise": 2,
		"affiliationchanged": 3, "superseded": 4, "cessationofoperation": 5,
		"certificatehold": 6, "removefromcrl": 8,
	}
	return codes[strings.ToLower(reason)]
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func moveIfExists(src, dst string) error {
	err := os.Rename(src, dst)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// writeFileAtomic writes data to a temp file in the same directory and
// renames it over path, so readers (OpenVPN, easyrsa) never see a partial file.
func writeFileAtomic(path string, data []byte, perm fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // no-op after a successful rename
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, perm); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
