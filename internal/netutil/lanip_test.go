package netutil

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestLANIPUsesOutboundPrivateIPv4(t *testing.T) {
	var network, address string
	dial := func(gotNetwork, gotAddress string) (net.Conn, error) {
		network, address = gotNetwork, gotAddress
		return fakeConn{local: &net.UDPAddr{IP: net.ParseIP("192.168.1.42"), Port: 54321}}, nil
	}

	got, err := lanIP(dial)
	if err != nil {
		t.Fatalf("lanIP() error: %v", err)
	}
	if got != "192.168.1.42" {
		t.Fatalf("lanIP() = %q, want %q", got, "192.168.1.42")
	}
	if network != "udp4" || address != routeProbeAddress {
		t.Fatalf("dialed %q %q, want udp4 %q", network, address, routeProbeAddress)
	}
}

func TestLANIPRejectsUnsafeAddresses(t *testing.T) {
	tests := []struct {
		name string
		addr net.Addr
		want string
	}{
		{name: "loopback", addr: &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, want: "non-private"},
		{name: "public", addr: &net.UDPAddr{IP: net.ParseIP("8.8.8.8")}, want: "non-private"},
		{name: "IPv6", addr: &net.UDPAddr{IP: net.ParseIP("fd00::1")}, want: "non-IPv4"},
		{name: "wrong address type", addr: fakeAddr("not-udp"), want: "no UDP address"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := lanIP(func(string, string) (net.Conn, error) {
				return fakeConn{local: tt.addr}, nil
			})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("lanIP() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestLANIPReportsDialFailure(t *testing.T) {
	want := errors.New("no route")
	_, err := lanIP(func(string, string) (net.Conn, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("lanIP() error = %v, want wrapped %v", err, want)
	}
}

func TestLANIPOnCurrentHost(t *testing.T) {
	got, err := LANIP()
	if err != nil {
		t.Skipf("no private outbound network available: %v", err)
	}
	ip := net.ParseIP(got)
	if ip == nil || ip.To4() == nil || !ip.IsPrivate() || ip.IsLoopback() {
		t.Fatalf("LANIP() returned unsafe address %q", got)
	}
}

type fakeConn struct {
	local net.Addr
}

func (c fakeConn) Read([]byte) (int, error)         { return 0, errors.New("not implemented") }
func (c fakeConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c fakeConn) Close() error                     { return nil }
func (c fakeConn) LocalAddr() net.Addr              { return c.local }
func (c fakeConn) RemoteAddr() net.Addr             { return fakeAddr(routeProbeAddress) }
func (c fakeConn) SetDeadline(time.Time) error      { return nil }
func (c fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c fakeConn) SetWriteDeadline(time.Time) error { return nil }

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }
