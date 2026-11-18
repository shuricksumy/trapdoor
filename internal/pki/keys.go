package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // SHA-1 is what RFC 5280 uses for key identifiers.
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// KeyType describes the algorithm of a key pair.
type KeyType struct {
	Algo    string // "ec", "rsa" or "ed"
	Curve   elliptic.Curve
	RSABits int
}

// DefaultKeyType is EC P-256, the most common modern OpenVPN setup.
var DefaultKeyType = KeyType{Algo: "ec", Curve: elliptic.P256()}

// keyTypeOf returns the key type of pub, so new keys match the CA.
func keyTypeOf(pub crypto.PublicKey) (KeyType, error) {
	switch k := pub.(type) {
	case *ecdsa.PublicKey:
		return KeyType{Algo: "ec", Curve: k.Curve}, nil
	case *rsa.PublicKey:
		return KeyType{Algo: "rsa", RSABits: k.N.BitLen()}, nil
	case ed25519.PublicKey:
		return KeyType{Algo: "ed"}, nil
	default:
		return KeyType{}, fmt.Errorf("unsupported key type %T", pub)
	}
}

func generateKey(kt KeyType) (crypto.Signer, error) {
	switch kt.Algo {
	case "ec":
		return ecdsa.GenerateKey(kt.Curve, rand.Reader)
	case "rsa":
		bits := kt.RSABits
		if bits < 2048 {
			bits = 2048
		}
		return rsa.GenerateKey(rand.Reader, bits)
	case "ed":
		_, k, err := ed25519.GenerateKey(rand.Reader)
		return k, err
	default:
		return nil, fmt.Errorf("unsupported key algorithm %q", kt.Algo)
	}
}

// signatureAlgorithm matches easyrsa's default digest (sha256).
func signatureAlgorithm(pub crypto.PublicKey) x509.SignatureAlgorithm {
	switch pub.(type) {
	case *ecdsa.PublicKey:
		return x509.ECDSAWithSHA256
	case *rsa.PublicKey:
		return x509.SHA256WithRSA
	case ed25519.PublicKey:
		return x509.PureEd25519
	default:
		return x509.UnknownSignatureAlgorithm
	}
}

// encodeKey returns the private key as PKCS#8 PEM, encrypted when
// passphrase is non-empty (PBES2, AES-256-CBC, like `openssl genpkey -aes256`).
func encodeKey(key crypto.Signer, passphrase string) ([]byte, error) {
	if passphrase == "" {
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
	}
	der, err := encryptPKCS8(key, []byte(passphrase))
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: der}), nil
}

// ErrKeyEncrypted is returned when a key is encrypted and no passphrase was given.
var ErrKeyEncrypted = errors.New("private key is encrypted")

// decodeKey parses PKCS#8 (plain or encrypted), PKCS#1 and SEC1 PEM keys.
func decodeKey(data []byte, passphrase string) (crypto.Signer, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	var (
		k   any
		err error
	)
	switch block.Type {
	case "PRIVATE KEY":
		k, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "ENCRYPTED PRIVATE KEY":
		if passphrase == "" {
			return nil, ErrKeyEncrypted
		}
		k, err = decryptPKCS8(block.Bytes, []byte(passphrase))
	case "RSA PRIVATE KEY":
		k, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		k, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unsupported PEM block %q", block.Type)
	}
	if err != nil {
		return nil, err
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, fmt.Errorf("key type %T cannot sign", k)
	}
	return s, nil
}

// decodeCert parses the first certificate in data. Leading text (openssl's
// human-readable dump in issued/*.crt) is skipped.
func decodeCert(data []byte) (*x509.Certificate, error) {
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, errors.New("no certificate PEM block found")
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

func encodeCert(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// newSerial returns a random positive 128-bit serial (32 hex digits),
// like easyrsa's default EASYRSA_RAND_SN.
func newSerial() (*big.Int, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	b[0] = b[0]&0x7f | 0x10 // positive, and always 32 hex digits
	return new(big.Int).SetBytes(b), nil
}

// serialHex formats a serial the way openssl writes it in index.txt.
func serialHex(n *big.Int) string {
	s := strings.ToUpper(n.Text(16))
	if len(s)%2 == 1 {
		s = "0" + s
	}
	return s
}

func parseSerial(s string) (*big.Int, error) {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		return nil, fmt.Errorf("invalid serial %q", s)
	}
	return n, nil
}

// subjectKeyID computes the RFC 5280 method 1 key identifier.
func subjectKeyID(pub crypto.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	var spki struct {
		Algorithm asn1.RawValue
		PublicKey asn1.BitString
	}
	if _, err := asn1.Unmarshal(der, &spki); err != nil {
		return nil, err
	}
	sum := sha1.Sum(spki.PublicKey.Bytes) //nolint:gosec // see import
	return sum[:], nil
}
