// Package security provides ephemeral identities for connector transport.
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// TLSIdentity is an ephemeral self-signed server identity. Its fingerprint
// is safe to advertise and is required to authenticate the TLS connection.
type TLSIdentity struct {
	Certificate tls.Certificate
	Fingerprint string
}

// NewTLSIdentity creates a short-lived ECDSA certificate and its SHA-256
// fingerprint. The private key is kept only in memory.
func NewTLSIdentity(hosts ...string) (*TLSIdentity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "LLMBeam connector"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              hosts,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create TLS certificate: %w", err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: template}
	digest := sha256.Sum256(der)
	return &TLSIdentity{Certificate: certificate, Fingerprint: hex.EncodeToString(digest[:])}, nil
}

// ServerConfig returns a TLS configuration for the connector listener.
func (identity *TLSIdentity) ServerConfig() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{identity.Certificate}, MinVersion: tls.VersionTLS13}
}

// NormalizeFingerprint accepts hex fingerprints with optional separators.
func NormalizeFingerprint(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(":", "", "-", "", " ", "").Replace(value)
	if len(value) != sha256.Size*2 {
		return "", errors.New("TLS fingerprint must be a SHA-256 hex digest")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", errors.New("TLS fingerprint must be hexadecimal")
	}
	return value, nil
}

// VerifyPeerCertificate returns a client callback that pins the leaf
// certificate fingerprint while still requiring TLS 1.3 encryption.
func VerifyPeerCertificate(expected string) (func([][]byte, [][]*x509.Certificate) error, error) {
	normalized, err := NormalizeFingerprint(expected)
	if err != nil {
		return nil, err
	}
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("TLS peer sent no certificate")
		}
		digest := sha256.Sum256(rawCerts[0])
		if !strings.EqualFold(hex.EncodeToString(digest[:]), normalized) {
			return errors.New("TLS fingerprint mismatch")
		}
		return nil
	}, nil
}
