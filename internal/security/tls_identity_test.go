package security

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
)

func TestTLSIdentityAndFingerprintPinning(t *testing.T) {
	identity, err := NewTLSIdentity("llmbeam.local")
	if err != nil {
		t.Fatal(err)
	}
	if len(identity.Fingerprint) != 64 {
		t.Fatalf("fingerprint length=%d", len(identity.Fingerprint))
	}
	cert, err := x509.ParseCertificate(identity.Certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	verify, err := VerifyPeerCertificate(identity.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := verify([][]byte{identity.Certificate.Certificate[0]}, nil); err != nil {
		t.Fatalf("matching fingerprint rejected: %v", err)
	}
	if err := verify([][]byte{cert.Raw[:len(cert.Raw)-1]}, nil); err == nil {
		t.Fatal("modified certificate accepted")
	}
	if identity.ServerConfig().MinVersion != tls.VersionTLS13 {
		t.Fatal("TLS identity permits old protocol versions")
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	input := strings.Repeat("ab", 32)
	got, err := NormalizeFingerprint(strings.ToUpper(input))
	if err != nil || got != input {
		t.Fatalf("normalized fingerprint=%q err=%v", got, err)
	}
	if _, err := NormalizeFingerprint("not-a-fingerprint"); err == nil {
		t.Fatal("invalid fingerprint accepted")
	}
}
