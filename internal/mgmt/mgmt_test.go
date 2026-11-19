package mgmt

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// Captured from OpenVPN 2.6.19.
const status3 = "TITLE\tOpenVPN 2.6.19 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [PKCS11] [MH/PKTINFO] [AEAD] [DCO]\r\n" +
	"TIME\t2026-11-18 15:45:41\t1795016741\r\n" +
	"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\tData Channel Cipher\r\n" +
	"CLIENT_LIST\talice\t127.0.0.1:37299\t10.98.0.2\t\t2520\t2212\t2026-11-18 15:45:36\t1795016736\tUNDEF\t1\t1\tAES-256-GCM\r\n" +
	"HEADER\tROUTING_TABLE\tVirtual Address\tCommon Name\tReal Address\tLast Ref\tLast Ref (time_t)\r\n" +
	"CLIENT_LIST\tUNDEF\t203.0.113.9:40100\t\t\t1640\t1020\t2026-11-18 15:45:40\t1795016740\tUNDEF\t2\t2\tN/A\r\n" +
	"ROUTING_TABLE\t10.98.0.2\talice\t127.0.0.1:37299\t2026-11-18 15:45:36\t1795016736\r\n" +
	"GLOBAL_STATS\tMax bcast/mcast queue length\t0\r\n" +
	"GLOBAL_STATS\tdco_enabled\t0\r\n" +
	"END\r\n"

// Older 2.4 layout: fewer columns, different order is handled via HEADER.
const status3v24 = "TITLE\tOpenVPN 2.4.12\n" +
	"TIME\tThu Jan  1 00:00:00 2026\t1767225600\n" +
	"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\n" +
	"CLIENT_LIST\tbob\t1.2.3.4:5000\t10.8.0.6\t\t100\t200\tThu Jan  1 00:00:00 2026\t1767225600\tbob-user\t7\t0\n" +
	"END\n"

// fakeServer answers management commands from a map, like OpenVPN does.
func fakeServer(t *testing.T, password string, answers map[string]string) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				r := bufio.NewReader(conn)
				if password != "" {
					_, _ = conn.Write([]byte("ENTER PASSWORD:"))
					pw, _ := r.ReadString('\n')
					if strings.TrimSpace(pw) != password {
						_, _ = conn.Write([]byte("ERROR: bad password\r\n"))
						return
					}
					_, _ = conn.Write([]byte("SUCCESS: password is correct\r\n"))
				}
				_, _ = conn.Write([]byte(">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\r\n"))
				for {
					cmd, err := r.ReadString('\n')
					if err != nil {
						return
					}
					cmd = strings.TrimSpace(cmd)
					if cmd == "quit" {
						return
					}
					ans, ok := answers[cmd]
					if !ok {
						ans = "ERROR: unknown command, enter 'help' for more options\r\n"
					}
					// An async notification in the middle must be ignored.
					_, _ = conn.Write([]byte(">BYTECOUNT_CLI:1,100,200\r\n" + ans))
				}
			}(conn)
		}
	}()
	return l.Addr().String()
}

func TestStatus(t *testing.T) {
	c := &Client{Addr: fakeServer(t, "", map[string]string{"status 3": status3})}
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Title, "OpenVPN 2.6.19") || st.Time.Unix() != 1795016741 {
		t.Errorf("title/time: %q %v", st.Title, st.Time)
	}
	if len(st.Sessions) != 1 {
		t.Fatalf("sessions = %+v", st.Sessions)
	}
	want := Session{
		ClientID: 1, PeerID: 1, Name: "alice", RealAddress: "127.0.0.1:37299",
		VirtualAddress: "10.98.0.2", BytesReceived: 2520, BytesSent: 2212,
		ConnectedSince: time.Unix(1795016736, 0).UTC(), Cipher: "AES-256-GCM",
	}
	if st.Sessions[0] != want {
		t.Errorf("session = %+v\nwant      %+v", st.Sessions[0], want)
	}
}

func TestStatusOldFormat(t *testing.T) {
	st, err := parseStatus(strings.Split(strings.TrimSuffix(status3v24, "END\n"), "\n"))
	if err != nil {
		t.Fatal(err)
	}
	s := st.Sessions[0]
	if s.Name != "bob" || s.ClientID != 7 || s.Username != "bob-user" || s.Cipher != "" || s.BytesSent != 200 {
		t.Errorf("session = %+v", s)
	}
}

func TestStatusNoClients(t *testing.T) {
	empty := "TITLE\tx\r\nHEADER\tCLIENT_LIST\tCommon Name\r\nEND\r\n"
	c := &Client{Addr: fakeServer(t, "", map[string]string{"status 3": empty})}
	st, err := c.Status(context.Background())
	if err != nil || st.Sessions == nil || len(st.Sessions) != 0 {
		t.Fatalf("st = %+v, err = %v (want empty, non-nil list)", st, err)
	}
}

func TestLoadStatsVersionKill(t *testing.T) {
	c := &Client{Addr: fakeServer(t, "", map[string]string{
		"load-stats":    "SUCCESS: nclients=1,bytesin=5039,bytesout=4568\r\n",
		"version":       "OpenVPN Version: OpenVPN 2.6.19 x86_64\r\nManagement Version: 5\r\nEND\r\n",
		"kill alice":    "SUCCESS: common name 'alice' found, 2 client(s) killed\r\n",
		"kill nobody":   "ERROR: common name 'nobody' not found\r\n",
		"client-kill 3": "SUCCESS: client-kill command succeeded\r\n",
	})}
	ctx := context.Background()

	ls, err := c.LoadStats(ctx)
	if err != nil || ls != (LoadStats{Clients: 1, BytesIn: 5039, BytesOut: 4568}) {
		t.Errorf("load-stats = %+v, %v", ls, err)
	}
	v, err := c.Version(ctx)
	if err != nil || v != "OpenVPN 2.6.19 x86_64" {
		t.Errorf("version = %q, %v", v, err)
	}
	if n, err := c.KillName(ctx, "alice"); err != nil || n != 2 {
		t.Errorf("kill alice = %d, %v", n, err)
	}
	if n, err := c.KillName(ctx, "nobody"); err != nil || n != 0 {
		t.Errorf("kill nobody = %d, %v", n, err)
	}
	if err := c.KillClient(ctx, 3); err != nil {
		t.Errorf("client-kill: %v", err)
	}
	if err := c.KillClient(ctx, 4); !errors.Is(err, ErrCommand) {
		t.Errorf("unknown client-kill: err = %v", err)
	}
	if _, err := c.KillName(ctx, "a b"); err == nil {
		t.Error("name with space must be rejected")
	}
}

func TestPassword(t *testing.T) {
	addr := fakeServer(t, "s3cret", map[string]string{"load-stats": "SUCCESS: nclients=0,bytesin=0,bytesout=0\r\n"})
	if _, err := (&Client{Addr: addr, Password: "s3cret"}).LoadStats(context.Background()); err != nil {
		t.Errorf("good password: %v", err)
	}
	if _, err := (&Client{Addr: addr, Password: "wrong"}).LoadStats(context.Background()); !errors.Is(err, ErrCommand) {
		t.Errorf("bad password: err = %v", err)
	}
}

func TestUnavailable(t *testing.T) {
	c := &Client{Addr: "127.0.0.1:1", Timeout: time.Second}
	if _, err := c.Status(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}
