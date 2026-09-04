// Package lan provides local-network discovery for LLMBeam connectors.
package lan

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"

	"github.com/llmbeam/llmbeam/internal/netutil"
)

const (
	ServiceType            = "_llmbeam._tcp"
	serviceDomain          = "local."
	defaultDiscoverTimeout = 2 * time.Second
	maxInstanceNameLength  = 63
)

// Peer describes an LLMBeam host discovered on the local network.
type Peer struct {
	Name        string
	Host        string
	Port        int
	TLSPort     int
	DeviceID    string
	Version     string
	Fingerprint string
	Addresses   []net.IP
}

// LANAdvertiser publishes an LLMBeam service over mDNS.
type LANAdvertiser interface {
	Start(name string, port int, metadata map[string]string) error
	Close() error
}

// LANDiscoverer finds LLMBeam services on the local network.
type LANDiscoverer interface {
	Discover(ctx context.Context) ([]Peer, error)
}

// Advertiser is the production mDNS advertiser.
type Advertiser struct {
	mu     sync.Mutex
	server *mdns.Server
}

// NewAdvertiser creates an mDNS advertiser.
func NewAdvertiser() *Advertiser {
	return &Advertiser{}
}

// DefaultDeviceName returns a stable human-readable name for this host.
func DefaultDeviceName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "LLMBeam"
	}
	return strings.TrimSpace(name)
}

// Start publishes a service. Only public discovery metadata is accepted;
// credentials and pairing material are intentionally never serialized.
func (a *Advertiser) Start(name string, port int, metadata map[string]string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}

	deviceID, err := randomDeviceID()
	if err != nil {
		return err
	}
	version := strings.TrimSpace(metadata["version"])
	tlsPort := strings.TrimSpace(metadata["tls_port"])
	fingerprint := strings.TrimSpace(metadata["fingerprint"])
	instance := instanceName(name, deviceID)
	ipText, err := netutil.LANIP()
	if err != nil {
		return fmt.Errorf("determine LAN address: %w", err)
	}
	ip := net.ParseIP(ipText)
	if ip == nil {
		return fmt.Errorf("invalid LAN address %q", ipText)
	}

	service, err := mdns.NewMDNSService(
		instance,
		ServiceType,
		serviceDomain,
		fmt.Sprintf("llmbeam-%s.local.", deviceID),
		port,
		[]net.IP{ip},
		publicTXT(deviceID, version, tlsPort, fingerprint),
	)
	if err != nil {
		return fmt.Errorf("create mDNS service: %w", err)
	}
	server, err := mdns.NewServer(&mdns.Config{Zone: service})
	if err != nil {
		return fmt.Errorf("start mDNS server: %w", err)
	}

	a.mu.Lock()
	old := a.server
	a.server = server
	a.mu.Unlock()
	if old != nil {
		_ = old.Shutdown()
	}
	return nil
}

// Close stops advertising. It is safe to call more than once.
func (a *Advertiser) Close() error {
	a.mu.Lock()
	server := a.server
	a.server = nil
	a.mu.Unlock()
	if server == nil {
		return nil
	}
	return server.Shutdown()
}

// Discoverer is the production mDNS resolver.
type Discoverer struct {
	Timeout time.Duration
}

// NewDiscoverer creates a resolver with a short bounded discovery window.
func NewDiscoverer() *Discoverer {
	return &Discoverer{Timeout: defaultDiscoverTimeout}
}

// Discover returns valid, deduplicated peers sorted by display name.
func (d *Discoverer) Discover(ctx context.Context) ([]Peer, error) {
	if ctx == nil {
		return nil, errors.New("nil discovery context")
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = defaultDiscoverTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries := make(chan *mdns.ServiceEntry, 32)
	params := mdns.DefaultParams(ServiceType)
	params.Timeout = timeout
	params.Entries = entries
	params.DisableIPv6 = false
	if err := mdns.QueryContext(queryCtx, params); err != nil {
		return nil, fmt.Errorf("discover mDNS services: %w", err)
	}

	peers := make(map[string]Peer)
	for {
		select {
		case entry := <-entries:
			if entry == nil {
				continue
			}
			peer, ok := peerFromEntry(entry)
			if !ok {
				continue
			}
			key := peer.DeviceID
			if key == "" {
				key = strings.Join([]string{peer.Name, peer.Host, fmt.Sprint(peer.Port)}, "|")
			}
			if existing, exists := peers[key]; !exists || peerLess(peer, existing) {
				peers[key] = peer
			}
		default:
			out := make([]Peer, 0, len(peers))
			for _, peer := range peers {
				out = append(out, peer)
			}
			sort.Slice(out, func(i, j int) bool { return peerLess(out[i], out[j]) })
			return out, nil
		}
	}
}

func peerFromEntry(entry *mdns.ServiceEntry) (Peer, bool) {
	metadata := make(map[string]string, len(entry.InfoFields))
	for _, field := range entry.InfoFields {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		metadata[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	deviceID := metadata["device_id"]
	if deviceID == "" || entry.Port < 1 || entry.Port > 65535 {
		return Peer{}, false
	}
	addresses := make([]net.IP, 0, 2)
	if isLANAddress(entry.AddrV4) {
		addresses = append(addresses, entry.AddrV4)
	}
	if isLANAddress(entry.AddrV6) {
		addresses = append(addresses, entry.AddrV6)
	}
	if len(addresses) == 0 {
		return Peer{}, false
	}
	tlsPort := 0
	if value, err := strconv.Atoi(metadata["tls_port"]); err == nil && value >= 1 && value <= 65535 {
		tlsPort = value
	}
	return Peer{
		Name:        serviceInstanceName(entry.Name),
		Host:        strings.TrimSuffix(entry.Host, "."),
		Port:        entry.Port,
		TLSPort:     tlsPort,
		DeviceID:    deviceID,
		Version:     metadata["version"],
		Fingerprint: metadata["fingerprint"],
		Addresses:   addresses,
	}, true
}

func serviceInstanceName(name string) string {
	name = strings.TrimSuffix(name, ".")
	suffix := "." + strings.TrimSuffix(ServiceType, ".") + "." + strings.TrimSuffix(serviceDomain, ".")
	return strings.TrimSuffix(name, suffix)
}

func peerLess(left, right Peer) bool {
	leftName, rightName := strings.ToLower(left.Name), strings.ToLower(right.Name)
	if leftName != rightName {
		return leftName < rightName
	}
	return left.DeviceID < right.DeviceID
}

func publicTXT(deviceID, version, tlsPort, fingerprint string) []string {
	txt := []string{"device_id=" + deviceID}
	if version != "" {
		txt = append(txt, "version="+version)
	}
	if tlsPort != "" {
		txt = append(txt, "tls_port="+tlsPort)
	}
	if fingerprint != "" {
		txt = append(txt, "fingerprint="+fingerprint)
	}
	return txt
}

func instanceName(name, deviceID string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name, _ = os.Hostname()
	}
	var builder strings.Builder
	for _, character := range name {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '-' || character == '_' || character == ' ' {
			builder.WriteRune(character)
		}
	}
	name = strings.TrimSpace(builder.String())
	if name == "" {
		name = "LLMBeam"
	}
	suffix := " (" + deviceID + ")"
	if len(name)+len(suffix) > maxInstanceNameLength {
		name = name[:maxInstanceNameLength-len(suffix)]
		name = strings.TrimSpace(name)
	}
	return name + suffix
}

func randomDeviceID() (string, error) {
	bytes := make([]byte, 6)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate device ID: %w", err)
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytes)), nil
}

func isLANAddress(ip net.IP) bool {
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return netutil.IsLocalIPv4(ip) || ip.IsLinkLocalUnicast()
}
