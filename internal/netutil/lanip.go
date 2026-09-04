// Package netutil provides local network helpers.
package netutil

import (
	"fmt"
	"net"
)

const routeProbeAddress = "192.0.2.1:9"

type dialFunc func(network, address string) (net.Conn, error)

// LANIP returns the local IPv4 address selected by the operating system's
// default outbound route. Establishing the UDP connection selects a route but
// does not send a packet to the probe address. In addition to RFC1918 space,
// the RFC2544 benchmarking range is accepted because it is commonly used by
// virtualized and VPN-backed development environments.
func LANIP() (string, error) {
	return lanIP(net.Dial)
}

func lanIP(dial dialFunc) (string, error) {
	conn, err := dial("udp4", routeProbeAddress)
	if err != nil {
		return "", fmt.Errorf("determine outbound route: %w", err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP == nil {
		return "", fmt.Errorf("outbound route returned no UDP address")
	}

	ip := addr.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("outbound route returned non-IPv4 address %q", addr.IP)
	}
	if !isLocalIPv4(ip) {
		return "", fmt.Errorf("outbound route returned non-private address %q", ip)
	}

	return ip.String(), nil
}

func isLocalIPv4(ip net.IP) bool {
	if ip == nil || ip.To4() == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if ip.IsPrivate() {
		return true
	}
	// RFC 2544 (198.18.0.0/15), used for isolated benchmark and VM networks.
	benchmarkRange := &net.IPNet{IP: net.ParseIP("198.18.0.0").To4(), Mask: net.CIDRMask(15, 32)}
	return benchmarkRange.Contains(ip)
}

// IsLocalIPv4 reports whether an address is suitable for local connector
// discovery and advertisement.
func IsLocalIPv4(ip net.IP) bool { return isLocalIPv4(ip) }
