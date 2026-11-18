package clientauth

// Codes must match oathtool, which openvpn-ui's auth_client.sh used
// ("oathtool --totp=SHA256 <hex key>"), so existing enrolments keep working.

import (
	"encoding/hex"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAgainstOathtool(t *testing.T) {
	if _, err := exec.LookPath("oathtool"); err != nil {
		t.Skip("oathtool not installed")
	}
	for i := 0; i < 5; i++ {
		key, _ := NewSecret()
		now := time.Now()
		out, err := exec.Command("oathtool", "--totp=SHA256", hex.EncodeToString(key)).Output()
		if err != nil {
			t.Fatal(err)
		}
		got := strings.TrimSpace(string(out))
		if got != Code(key, now) && got != Code(key, now.Add(-Period)) {
			t.Fatalf("oathtool %s, ours %s", got, Code(key, now))
		}
	}
}
