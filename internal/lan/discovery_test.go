package lan

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/mdns"
)

func TestPublicTXTDoesNotExposeSecrets(t *testing.T) {
	txt := publicTXT("device-123", "0.1.0")
	joined := strings.Join(txt, "|")
	for _, secret := range []string{"code", "token", "api_key", "tailcat"} {
		if strings.Contains(strings.ToLower(joined), secret) {
			t.Fatalf("TXT metadata contains secret marker %q: %q", secret, joined)
		}
	}
	if !reflect.DeepEqual(txt, []string{"device_id=device-123", "version=0.1.0"}) {
		t.Fatalf("unexpected TXT metadata: %#v", txt)
	}
}

func TestInstanceNameIsUniqueAndBounded(t *testing.T) {
	first := instanceName("My Mac / Office", "abc123")
	second := instanceName("My Mac / Office", "def456")
	if first == second || !strings.Contains(first, "abc123") || strings.Contains(first, "/") {
		t.Fatalf("unexpected instance names: %q, %q", first, second)
	}
	if len(first) > maxInstanceNameLength {
		t.Fatalf("instance name exceeds DNS label limit: %d", len(first))
	}
}

func TestPeerFromEntryFiltersAndParsesMetadata(t *testing.T) {
	entry := &mdns.ServiceEntry{
		Name:       "Laptop (abc)._llmbeam._tcp.local.",
		Host:       "llmbeam-abc.local.",
		Port:       8442,
		AddrV4:     net.ParseIP("192.168.1.20"),
		InfoFields: []string{"device_id=abc", "version=0.2.0"},
	}
	peer, ok := peerFromEntry(entry)
	if !ok || peer.Name != "Laptop (abc)" || peer.Port != 8442 || peer.Version != "0.2.0" {
		t.Fatalf("unexpected peer: %#v (ok=%v)", peer, ok)
	}

	entry.AddrV4 = net.ParseIP("8.8.8.8")
	if _, ok := peerFromEntry(entry); ok {
		t.Fatal("accepted non-LAN address")
	}

	entry.AddrV4 = net.ParseIP("192.168.1.20")
	entry.InfoFields = []string{"version=0.2.0"}
	if _, ok := peerFromEntry(entry); ok {
		t.Fatal("accepted entry without device ID")
	}
}

func TestDiscovererRejectsNilContext(t *testing.T) {
	if _, err := (&Discoverer{}).Discover(context.Context(nil)); err == nil {
		t.Fatal("expected nil context error")
	}
}
