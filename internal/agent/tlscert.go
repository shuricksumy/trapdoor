package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"strings"
	"time"
)

// An agent on another host listens on TCP with TLS (TD_AGENT_TLS=auto).
// Its certificate is self-signed and made on first start; the web process
// pins its SHA-256 fingerprint (like an SSH host key), so no CA is needed.

// AgentCert loads the agent's TLS certificate, creating it when the files
// do not exist, and returns it with its fingerprint.
func AgentCert(certPath, keyPath string) (tls.Certificate, string, error) {
	if _, err := os.Stat(certPath); errors.Is(err, fs.ErrNotExist) {
		if err := newAgentCert(certPath, keyPath); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return c, Fingerprint(c.Certificate[0]), nil
}

// Fingerprint is "sha256:<hex>" of a DER certificate.
func Fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// NormalizeFingerprint accepts "sha256:<hex>", plain hex and hex with
// colons (as openssl prints it).
func NormalizeFingerprint(s string) (string, error) {
	h := strings.ToLower(strings.TrimSpace(s))
	h = strings.TrimPrefix(h, "sha256:")
	h = strings.TrimPrefix(h, "sha256 fingerprint=")
	h = strings.ReplaceAll(h, ":", "")
	if b, err := hex.DecodeString(h); err != nil || len(b) != sha256.Size {
		return "", errors.New("fingerprint must be the SHA-256 printed by `trapdoor agent-cert` (sha256:<64 hex digits>)")
	}
	return "sha256:" + h, nil
}

func newAgentCert(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "trapdoor agent " + host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().AddDate(20, 0, 0), // pinned, not checked for expiry
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", keyPath, err)
	}
	return os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
}

// pinnedTLS trusts exactly the certificate with the given fingerprint.
func pinnedTLS(fingerprint string) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // #nosec G402 -- replaced by the pin check below
		VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 || Fingerprint(raw[0]) != fingerprint {
				got := "none"
				if len(raw) > 0 {
					got = Fingerprint(raw[0])
				}
				return fmt.Errorf("agent certificate %s does not match the pinned %s", got, fingerprint)
			}
			return nil
		},
	}
}
