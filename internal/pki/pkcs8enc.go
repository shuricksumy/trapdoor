package pki

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // only accepted as PBKDF2 PRF for old keys
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"
)

// Encrypted PKCS#8 (RFC 5958 / RFC 8018 PBES2), the format written by
// `openssl genpkey -aes256` and therefore by easyrsa for passphrase keys.
// Only the stdlib is used: PBKDF2 with HMAC-SHA1/256/512 and AES-CBC.

var (
	oidPBES2      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACSHA1   = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACSHA256 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidHMACSHA512 = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 11}
	oidAES128CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

const pbkdf2Iterations = 600_000 // OWASP 2023 recommendation for HMAC-SHA256

type encryptedPrivateKeyInfo struct {
	Algorithm     pkix.AlgorithmIdentifier
	EncryptedData []byte
}

type pbes2Params struct {
	KeyDerivationFunc pkix.AlgorithmIdentifier
	EncryptionScheme  pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                      `asn1:"optional"`
	PRF        pkix.AlgorithmIdentifier `asn1:"optional"`
}

// ErrBadPassphrase is returned when an encrypted key cannot be decrypted.
var ErrBadPassphrase = errors.New("wrong passphrase or corrupt key")

func encryptPKCS8(key any, passphrase []byte) ([]byte, error) {
	plain, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	dk, err := pbkdf2.Key(sha256.New, string(passphrase), salt, pbkdf2Iterations, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plain, aes.BlockSize)
	enc := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, padded)

	kdf, err := asn1.Marshal(pbkdf2Params{
		Salt:       salt,
		Iterations: pbkdf2Iterations,
		PRF:        pkix.AlgorithmIdentifier{Algorithm: oidHMACSHA256, Parameters: asn1.NullRawValue},
	})
	if err != nil {
		return nil, err
	}
	ivDER, err := asn1.Marshal(iv)
	if err != nil {
		return nil, err
	}
	params, err := asn1.Marshal(pbes2Params{
		KeyDerivationFunc: pkix.AlgorithmIdentifier{Algorithm: oidPBKDF2, Parameters: asn1.RawValue{FullBytes: kdf}},
		EncryptionScheme:  pkix.AlgorithmIdentifier{Algorithm: oidAES256CBC, Parameters: asn1.RawValue{FullBytes: ivDER}},
	})
	if err != nil {
		return nil, err
	}
	return asn1.Marshal(encryptedPrivateKeyInfo{
		Algorithm:     pkix.AlgorithmIdentifier{Algorithm: oidPBES2, Parameters: asn1.RawValue{FullBytes: params}},
		EncryptedData: enc,
	})
}

func decryptPKCS8(der, passphrase []byte) (any, error) {
	var info encryptedPrivateKeyInfo
	if rest, err := asn1.Unmarshal(der, &info); err != nil || len(rest) > 0 {
		return nil, fmt.Errorf("parse encrypted key: %v", err)
	}
	if !info.Algorithm.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("unsupported key encryption %v (only PBES2)", info.Algorithm.Algorithm)
	}
	var p pbes2Params
	if _, err := asn1.Unmarshal(info.Algorithm.Parameters.FullBytes, &p); err != nil {
		return nil, fmt.Errorf("parse PBES2 params: %w", err)
	}
	if !p.KeyDerivationFunc.Algorithm.Equal(oidPBKDF2) {
		return nil, fmt.Errorf("unsupported KDF %v (only PBKDF2)", p.KeyDerivationFunc.Algorithm)
	}
	var kp pbkdf2Params
	if _, err := asn1.Unmarshal(p.KeyDerivationFunc.Parameters.FullBytes, &kp); err != nil {
		return nil, fmt.Errorf("parse PBKDF2 params: %w", err)
	}
	var prf func() hash.Hash
	switch {
	case len(kp.PRF.Algorithm) == 0, kp.PRF.Algorithm.Equal(oidHMACSHA1):
		prf = sha1.New
	case kp.PRF.Algorithm.Equal(oidHMACSHA256):
		prf = sha256.New
	case kp.PRF.Algorithm.Equal(oidHMACSHA512):
		prf = sha512.New
	default:
		return nil, fmt.Errorf("unsupported PRF %v", kp.PRF.Algorithm)
	}
	var keyLen int
	switch {
	case p.EncryptionScheme.Algorithm.Equal(oidAES128CBC):
		keyLen = 16
	case p.EncryptionScheme.Algorithm.Equal(oidAES192CBC):
		keyLen = 24
	case p.EncryptionScheme.Algorithm.Equal(oidAES256CBC):
		keyLen = 32
	default:
		return nil, fmt.Errorf("unsupported cipher %v", p.EncryptionScheme.Algorithm)
	}
	var iv []byte
	if _, err := asn1.Unmarshal(p.EncryptionScheme.Parameters.FullBytes, &iv); err != nil || len(iv) != aes.BlockSize {
		return nil, errors.New("invalid AES IV")
	}
	if len(info.EncryptedData) == 0 || len(info.EncryptedData)%aes.BlockSize != 0 {
		return nil, ErrBadPassphrase
	}
	dk, err := pbkdf2.Key(prf, string(passphrase), kp.Salt, kp.Iterations, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(dk)
	if err != nil {
		return nil, err
	}
	plain := make([]byte, len(info.EncryptedData))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(plain, info.EncryptedData)
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return nil, ErrBadPassphrase
	}
	k, err := x509.ParsePKCS8PrivateKey(plain)
	if err != nil {
		return nil, ErrBadPassphrase
	}
	return k, nil
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	return append(bytes.Clone(b), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte, size int) ([]byte, error) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, errors.New("bad padding")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > size {
		return nil, errors.New("bad padding")
	}
	pad := bytes.Repeat([]byte{byte(n)}, n)
	if !hmac.Equal(b[len(b)-n:], pad) {
		return nil, errors.New("bad padding")
	}
	return b[:len(b)-n], nil
}
