// Package pki implements the certificate authority operations tunnelward needs.
//
// The implementation is pure Go (crypto/x509) and keeps the same layout on
// disk as easyrsa 3.x, so existing openvpn-ui / easyrsa PKIs work without
// re-issuing certificates, and the easyrsa CLI keeps working on a PKI that
// tunnelward manages:
//
//	ca.crt, private/ca.key
//	issued/<name>.crt, private/<name>.key, reqs/<name>.req
//	certs_by_serial/<SERIAL>.pem
//	revoked/{certs,private,reqs}_by_serial/<SERIAL>.{crt,key,req}
//	renewed/issued/<name>.crt
//	index.txt, index.txt.attr, serial, crl.pem
//
// Differences from easyrsa, by design:
//   - Renew revokes the previous certificate (reason "superseded"); easyrsa
//     leaves it valid until "revoke-renewed" is run.
//   - Remove keeps revoked index entries until they expire, so removed
//     certificates stay in the CRL.
package pki

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// Status of a certificate.
type Status string

const (
	StatusValid   Status = "valid"
	StatusRevoked Status = "revoked"
	StatusExpired Status = "expired"
)

// Kind of certificate to issue.
type Kind string

const (
	KindClient Kind = "client"
	KindServer Kind = "server"
)

// Cert describes one certificate entry of the PKI.
type Cert struct {
	Name         string    `json:"name"` // common name
	Serial       string    `json:"serial"`
	Status       Status    `json:"status"`
	NotAfter     time.Time `json:"not_after"`
	RevokedAt    time.Time `json:"revoked_at,omitzero"`
	RevokeReason string    `json:"revoke_reason,omitempty"`
	// Current is true for the newest entry of a name. Older entries are the
	// history (previous, renewed or revoked certificates).
	Current bool `json:"current"`
}

// IssueRequest asks for a new certificate.
type IssueRequest struct {
	Name string
	// Kind defaults to KindClient.
	Kind Kind
	// Passphrase encrypts the private key (PKCS#8) when non-empty.
	Passphrase string
	// Validity of the certificate; zero means the PKI default.
	Validity time.Duration
}

// Errors returned by the PKI; check with errors.Is.
var (
	ErrNotFound       = errors.New("certificate not found")
	ErrAlreadyExists  = errors.New("certificate already exists")
	ErrInvalidName    = errors.New("invalid certificate name")
	ErrNotValid       = errors.New("certificate is not valid")
	ErrNotRevoked     = errors.New("certificate is not revoked")
	ErrNotInitialized = errors.New("PKI is not initialized")
)

// PKI is the set of operations the agent performs on the certificate authority.
type PKI interface {
	// List returns every index entry, oldest first.
	List(ctx context.Context) ([]Cert, error)
	// Get returns the current (newest) entry for name.
	Get(ctx context.Context, name string) (Cert, error)
	Issue(ctx context.Context, req IssueRequest) (Cert, error)
	Revoke(ctx context.Context, name string) error
	Unrevoke(ctx context.Context, name string) error
	// Renew issues a new certificate for an existing name with the same key,
	// also when the current one has expired. The old certificate is revoked
	// as superseded.
	Renew(ctx context.Context, name string, validity time.Duration) (Cert, error)
	// Remove revokes the certificate if needed and deletes its key material.
	Remove(ctx context.Context, name string) error
	// GenCRL regenerates crl.pem.
	GenCRL(ctx context.Context) error
}

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)

// ValidName reports whether name is safe to use as a certificate name.
// It allows the characters OpenVPN keeps in common names and never anything
// that could form a path.
func ValidName(name string) bool {
	return validName.MatchString(name) && name != "ca"
}
