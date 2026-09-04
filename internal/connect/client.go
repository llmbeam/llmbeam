// Package connect implements the native LAN connector client.
package connect

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/llmbeam/llmbeam/internal/security"
)

const defaultHTTPTimeout = 10 * time.Second

// Session is the credential returned after a successful one-time pairing.
type Session struct {
	Token       string    `json:"token"`
	DeviceID    string    `json:"device_id"`
	Expires     time.Time `json:"expires_at"`
	Fingerprint string    `json:"server_fingerprint,omitempty"`
}

// Model describes a model exposed by the paired host's OpenAI endpoint.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

// Client talks to a paired LLMBeam host.
type Client struct {
	baseURL     string
	http        *http.Client
	session     Session
	localAPIKey string
	fingerprint string
}

// New creates a client for a host URL.
func New(baseURL string, httpClient *http.Client) (*Client, error) {
	return newClient(baseURL, httpClient, "")
}

// NewPinned creates a client that authenticates a self-signed TLS host by its
// SHA-256 certificate fingerprint.
func NewPinned(baseURL, fingerprint string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme != "https" {
		return nil, errors.New("pinned connector host must use HTTPS")
	}
	normalized, err := security.NormalizeFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	baseTransport, ok := transport.(*http.Transport)
	if !ok {
		return nil, errors.New("pinned connector requires an HTTP transport")
	}
	clone := baseTransport.Clone()
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.InsecureSkipVerify = true
	clone.TLSClientConfig.MinVersion = tls.VersionTLS13
	clone.TLSClientConfig.VerifyPeerCertificate, err = security.VerifyPeerCertificate(normalized)
	if err != nil {
		return nil, err
	}
	pinnedClient := *httpClient
	pinnedClient.Transport = clone
	client, err := newClient(baseURL, &pinnedClient, normalized)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func newClient(baseURL string, httpClient *http.Client, fingerprint string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("host must be an HTTP(S) URL without a path")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	localAPIKey, err := randomToken("lb_", 32)
	if err != nil {
		return nil, err
	}
	return &Client{baseURL: baseURL, http: httpClient, localAPIKey: localAPIKey, fingerprint: fingerprint}, nil
}

// Pair exchanges a one-time Connect Code for a connector session.
func (c *Client) Pair(ctx context.Context, code string) (Session, error) {
	if ctx == nil {
		return Session{}, errors.New("nil pairing context")
	}
	clientID, err := randomID("b_")
	if err != nil {
		return Session{}, err
	}
	publicKey, err := randomID("pk_")
	if err != nil {
		return Session{}, err
	}
	payload, err := json.Marshal(map[string]string{
		"code":              strings.TrimSpace(code),
		"client_id":         clientID,
		"client_public_key": publicKey,
	})
	if err != nil {
		return Session{}, fmt.Errorf("encode pairing request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/connector/pair", bytes.NewReader(payload))
	if err != nil {
		return Session{}, fmt.Errorf("create pairing request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Session{}, fmt.Errorf("host unreachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized {
			return Session{}, errors.New("pairing rejected: Connect Code is invalid or expired")
		}
		if response.StatusCode >= 500 {
			return Session{}, fmt.Errorf("host returned server error (%s)", response.Status)
		}
		return Session{}, fmt.Errorf("pairing rejected by host (%s)", response.Status)
	}
	var session Session
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode pairing response: %w", err)
	}
	if session.Token == "" || session.Expires.IsZero() {
		return Session{}, errors.New("host returned an invalid connector session")
	}
	if c.fingerprint != "" && !strings.EqualFold(c.fingerprint, session.Fingerprint) {
		return Session{}, errors.New("TLS fingerprint mismatch")
	}
	c.session = session
	return session, nil
}

// Refresh rotates the current connector token.
func (c *Client) Refresh(ctx context.Context) (Session, error) {
	if c.session.Token == "" {
		return Session{}, errors.New("client is not paired")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/connector/refresh", strings.NewReader("{}"))
	if err != nil {
		return Session{}, err
	}
	request.Header.Set("Authorization", "Bearer "+c.session.Token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return Session{}, fmt.Errorf("refresh connector session: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Session{}, fmt.Errorf("refresh connector session rejected (%s)", response.Status)
	}
	var session Session
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&session); err != nil {
		return Session{}, fmt.Errorf("decode refresh response: %w", err)
	}
	if c.fingerprint != "" && !strings.EqualFold(c.fingerprint, session.Fingerprint) {
		return Session{}, errors.New("TLS fingerprint mismatch")
	}
	c.session = session
	return session, nil
}

// Models returns the models currently available on the paired host.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	if ctx == nil {
		return nil, errors.New("nil models context")
	}
	if c.session.Token == "" {
		return nil, errors.New("client is not paired")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("create models request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.session.Token)
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("retrieve models: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized {
			return nil, errors.New("retrieve models rejected: connector session is invalid or expired")
		}
		return nil, fmt.Errorf("retrieve models rejected (%s)", response.Status)
	}
	var payload struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	for _, model := range payload.Data {
		if strings.TrimSpace(model.ID) == "" {
			return nil, errors.New("host returned a model without an id")
		}
	}
	return payload.Data, nil
}

// Token returns the current connector token.
func (c *Client) Token() string { return c.session.Token }

// APIKey returns the ephemeral key required by the local proxy.
func (c *Client) APIKey() string { return c.localAPIKey }

// BaseURL returns the paired host URL.
func (c *Client) BaseURL() string { return c.baseURL }

func randomID(prefix string) (string, error) {
	return randomToken(prefix, 12)
}

func randomToken(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate connector identity: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
