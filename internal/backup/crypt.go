// Package backup writes and reads trapdoor backups: a gzipped tar of the
// agent's directories and the web database, optionally encrypted with a
// passphrase.
package backup

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// An encrypted backup is:
//
//	header: "TDBACKUP" | version 1 | iterations uint32 | salt 16 | nonce prefix 7
//	chunks: length uint32 | AES-256-GCM(chunk), nonce = prefix | counter uint32 | last 0/1
//
// The key is PBKDF2-SHA256 of the passphrase. Every chunk authenticates the
// header; the counter and the last flag stop reordering and truncation.

const (
	magic      = "TDBACKUP"
	version    = 1
	iterations = 600_000
	chunkSize  = 64 << 10
	headerLen  = len(magic) + 1 + 4 + 16 + 7
)

// ErrPassphrase is returned for a wrong passphrase or a damaged file.
var ErrPassphrase = errors.New("wrong passphrase, or the backup is damaged")

// Encrypted reports whether the start of a file is an encrypted backup.
func Encrypted(start []byte) bool { return bytes.HasPrefix(start, []byte(magic)) }

func gcm(passphrase string, salt []byte, iter int) (cipher.AEAD, error) {
	key, err := pbkdf2.Key(sha256.New, passphrase, salt, iter, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

type encWriter struct {
	w       io.Writer
	aead    cipher.AEAD
	header  []byte
	prefix  []byte
	counter uint32
	buf     []byte
}

// NewEncrypter returns a writer that encrypts into w; Close writes the last
// chunk (and does not close w).
func NewEncrypter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	if passphrase == "" {
		return nil, errors.New("empty passphrase")
	}
	h := make([]byte, headerLen)
	copy(h, magic)
	h[len(magic)] = version
	binary.BigEndian.PutUint32(h[len(magic)+1:], iterations)
	if _, err := rand.Read(h[len(magic)+5:]); err != nil { // salt and nonce prefix
		return nil, err
	}
	salt := h[len(magic)+5 : len(magic)+21]
	aead, err := gcm(passphrase, salt, iterations)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(h); err != nil {
		return nil, err
	}
	return &encWriter{w: w, aead: aead, header: h, prefix: h[len(magic)+21:], buf: make([]byte, 0, chunkSize)}, nil
}

func (e *encWriter) nonce(last bool) []byte {
	n := make([]byte, 12)
	copy(n, e.prefix)
	binary.BigEndian.PutUint32(n[7:], e.counter)
	if last {
		n[11] = 1
	}
	return n
}

func (e *encWriter) flush(last bool) error {
	if e.counter == ^uint32(0) {
		return errors.New("backup too large")
	}
	ct := e.aead.Seal(nil, e.nonce(last), e.buf, e.header)
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(ct))) // #nosec G115 -- at most chunkSize + 16
	if _, err := e.w.Write(l[:]); err != nil {
		return err
	}
	if _, err := e.w.Write(ct); err != nil {
		return err
	}
	e.counter++
	e.buf = e.buf[:0]
	return nil
}

func (e *encWriter) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(chunkSize-len(e.buf), len(p))
		e.buf = append(e.buf, p[:k]...)
		p, n = p[k:], n+k
		if len(e.buf) == chunkSize {
			if err := e.flush(false); err != nil {
				return n, err
			}
		}
	}
	return n, nil
}

func (e *encWriter) Close() error { return e.flush(true) }

type decReader struct {
	r       *bufio.Reader
	aead    cipher.AEAD
	header  []byte
	prefix  []byte
	counter uint32
	buf     []byte
	done    bool
}

// NewDecrypter reads an encrypted backup from r.
func NewDecrypter(r io.Reader, passphrase string) (io.Reader, error) {
	h := make([]byte, headerLen)
	if _, err := io.ReadFull(r, h); err != nil || !Encrypted(h) {
		return nil, errors.New("not an encrypted trapdoor backup")
	}
	if h[len(magic)] != version {
		return nil, fmt.Errorf("backup format %d is newer than this trapdoor", h[len(magic)])
	}
	iter := int(binary.BigEndian.Uint32(h[len(magic)+1:]))
	if iter < 100_000 || iter > 10_000_000 {
		return nil, ErrPassphrase
	}
	aead, err := gcm(passphrase, h[len(magic)+5:len(magic)+21], iter)
	if err != nil {
		return nil, err
	}
	return &decReader{r: bufio.NewReader(r), aead: aead, header: h, prefix: h[len(magic)+21:]}, nil
}

func (d *decReader) Read(p []byte) (int, error) {
	for len(d.buf) == 0 {
		if d.done {
			return 0, io.EOF
		}
		var l [4]byte
		if _, err := io.ReadFull(d.r, l[:]); err != nil {
			return 0, ErrPassphrase // truncated: the last chunk never came
		}
		n := binary.BigEndian.Uint32(l[:])
		if n > chunkSize+uint32(d.aead.Overhead()) {
			return 0, ErrPassphrase
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(d.r, ct); err != nil {
			return 0, ErrPassphrase
		}
		nonce := make([]byte, 12)
		copy(nonce, d.prefix)
		binary.BigEndian.PutUint32(nonce[7:], d.counter)
		pt, err := d.aead.Open(nil, nonce, ct, d.header)
		if err != nil {
			nonce[11] = 1
			if pt, err = d.aead.Open(nil, nonce, ct, d.header); err != nil {
				return 0, ErrPassphrase
			}
			d.done = true
			if _, err := d.r.Peek(1); err != io.EOF {
				return 0, ErrPassphrase // data after the last chunk
			}
		}
		d.counter++
		d.buf = pt
	}
	n := copy(p, d.buf)
	d.buf = d.buf[n:]
	return n, nil
}
