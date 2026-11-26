package clientnet

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// fakeDockerDNS answers A queries for names in table and nothing else, like
// Docker's embedded DNS answering a container name.
func fakeDockerDNS(t *testing.T, table map[string]netip.Addr) {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	prev := dockerDNS
	dockerDNS = conn.LocalAddr().String()
	t.Cleanup(func() {
		dockerDNS = prev
		_ = conn.Close()
	})
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			q := buf[:n]
			if len(q) < 12 {
				continue
			}
			// Question: labels from offset 12, then type and class.
			var labels []string
			i := 12
			for i < len(q) && q[i] != 0 {
				l := int(q[i])
				labels = append(labels, string(q[i+1:i+1+l]))
				i += 1 + l
			}
			qend := i + 5
			if qend > len(q) {
				continue
			}
			qtype := binary.BigEndian.Uint16(q[i+1 : i+3])
			name := strings.Join(labels, ".")
			resp := append([]byte{}, q[:qend]...)
			resp[2], resp[3] = 0x81, 0x80 // response, recursion available, NOERROR
			resp[4], resp[5] = 0, 1
			resp[8], resp[9], resp[10], resp[11] = 0, 0, 0, 0
			addr, ok := table[name]
			if ok && qtype == 1 && addr.Is4() {
				resp[6], resp[7] = 0, 1
				a := addr.As4()
				resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, a[0], a[1], a[2], a[3])
			} else {
				resp[6], resp[7] = 0, 0
				if !ok {
					resp[3] = 0x83 // NXDOMAIN
				}
			}
			_, _ = conn.WriteTo(resp, from)
		}
	}()
}

func TestLookupProxyHostFallsBackToDockerDNS(t *testing.T) {
	fakeDockerDNS(t, map[string]netip.Addr{"npm-proxy-test.invalid": netip.MustParseAddr("172.18.0.2")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The system resolver cannot know this name; Docker's server can.
	addrs, err := LookupProxyHost(ctx, "npm-proxy-test.invalid")
	if err != nil || len(addrs) != 1 || addrs[0] != netip.MustParseAddr("172.18.0.2") {
		t.Fatalf("LookupProxyHost = %v, %v; want 172.18.0.2 from Docker DNS", addrs, err)
	}
	if _, err := LookupProxyHost(ctx, "nobody-proxy-test.invalid"); err == nil {
		t.Fatal("a name neither resolver knows must fail")
	}
}
