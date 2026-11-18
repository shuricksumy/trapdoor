// Package pki defines the certificate authority operations tunnelward needs.
//
// The default implementation (coming next) is pure Go (crypto/x509) and keeps
// an easyrsa-compatible layout on disk:
//
//	pki/ca.crt, pki/private/ca.key
//	pki/issued/<name>.crt, pki/private/<name>.key, pki/reqs/<name>.req
//	pki/index.txt, pki/serial, pki/crl.pem
//
// so existing openvpn-ui / easyrsa PKIs can be used without re-issuing certs.
package pki

import (
	"context"
	"errors"
	"time"
)

// Status of a certificate as recorded in index.txt.
type Status string

const (
	StatusValid   Status = "V"
	StatusRevoked Status = "R"
	StatusExpired Status = "E"
)

// Cert describes one certificate in the PKI.
type Cert struct {
	Name      string    `json:"name"` // common name
	Serial    string    `json:"serial"`
	Status    Status    `json:"status"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
	RevokedAt time.Time `json:"revoked_at,omitzero"`
}

// IssueRequest asks for a new client certificate.
type IssueRequest struct {
	Name string
	// Passphrase encrypts the private key (PKCS#8) when non-empty.
	Passphrase string
	// Validity of the certificate; zero means the PKI default.
	Validity time.Duration
}

// Errors returned by implementations; check with errors.Is.
var (
	ErrNotFound      = errors.New("certificate not found")
	ErrAlreadyExists = errors.New("certificate already exists")
	ErrInvalidName   = errors.New("invalid certificate name")
)

// PKI is the set of operations the agent performs on the certificate authority.
type PKI interface {
	List(ctx context.Context) ([]Cert, error)
	Get(ctx context.Context, name string) (Cert, error)
	Issue(ctx context.Context, req IssueRequest) (Cert, error)
	Revoke(ctx context.Context, name string) error
	Unrevoke(ctx context.Context, name string) error
	// Renew issues a new certificate for an existing name, also when the
	// current one has expired. The old certificate is revoked.
	Renew(ctx context.Context, name string, validity time.Duration) (Cert, error)
	// Remove deletes all files for name and its index.txt entry.
	Remove(ctx context.Context, name string) error
	// GenCRL regenerates crl.pem.
	GenCRL(ctx context.Context) error
}
