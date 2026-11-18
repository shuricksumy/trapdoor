package clientauth

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shuricksumy/tunnelward/internal/pki"
)

// VerifyFile is `tunnelward auth-verify`: OpenVPN's auth-user-pass-verify
// with via-file. passFile holds username and password on two lines;
// commonName is the certificate's CN (OpenVPN's $common_name).
//
// The username must equal the certificate name, so one client's password
// can never be used with another client's certificate.
func VerifyFile(ccdDir, passFile, commonName string, now time.Time) (string, error) {
	f, err := os.Open(passFile)
	if err != nil {
		return "", fmt.Errorf("read credentials: %w", err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	var lines []string
	for sc.Scan() && len(lines) < 2 {
		lines = append(lines, strings.TrimRight(sc.Text(), "\r"))
	}
	if len(lines) == 0 {
		return "", errors.New("empty credentials file")
	}
	user := lines[0]
	pass := ""
	if len(lines) > 1 {
		pass = lines[1]
	}

	if !pki.ValidName(user) {
		return user, fmt.Errorf("%w: invalid username", ErrDenied)
	}
	if commonName != "" && user != commonName {
		return user, fmt.Errorf("%w: username %q does not match certificate %q", ErrDenied, user, commonName)
	}
	data, err := os.ReadFile(filepath.Join(ccdDir, user))
	if errors.Is(err, fs.ErrNotExist) {
		return user, fmt.Errorf("%w: no ccd file for %s", ErrDenied, user)
	}
	if err != nil {
		return user, err
	}
	s, err := Parse(string(data))
	if err != nil {
		return user, fmt.Errorf("%w: %v", ErrDenied, err)
	}
	if err := Verify(s, pass, now); err != nil {
		return user, fmt.Errorf("%w: wrong password or code", ErrDenied)
	}
	return user, nil
}
