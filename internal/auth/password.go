// Package auth implements admin users, password hashing, login sessions and
// login throttling for the web process.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// PBKDF2-HMAC-SHA256 with 600k iterations (OWASP 2023), stdlib only.
// Stored as: pbkdf2-sha256$<iterations>$<salt b64>$<hash b64>
const (
	hashScheme     = "pbkdf2-sha256"
	hashIterations = 600_000
	saltLen        = 16
	keyLen         = 32

	MinPasswordLen = 10
	MaxPasswordLen = 256
)

// ErrWeakPassword is returned for passwords outside the length limits.
var ErrWeakPassword = fmt.Errorf("password must be %d-%d characters", MinPasswordLen, MaxPasswordLen)

// CheckPassword enforces the password policy.
func CheckPassword(pw string) error {
	if n := utf8.RuneCountInString(pw); n < MinPasswordLen || n > MaxPasswordLen {
		return ErrWeakPassword
	}
	return nil
}

// HashPassword returns an encoded hash of pw.
func HashPassword(pw string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, hashIterations, keyLen)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("%s$%d$%s$%s", hashScheme, hashIterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// VerifyPassword reports whether pw matches the encoded hash, in constant time.
func VerifyPassword(encoded, pw string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return false, errors.New("unknown password hash format")
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter < 1 {
		return false, errors.New("bad iteration count")
	}
	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil {
		return false, err
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil {
		return false, err
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified for unknown users so their response time matches.
var dummyHash = func() string {
	h, err := HashPassword("not-a-real-password-just-for-timing")
	if err != nil {
		panic(err)
	}
	return h
}()
