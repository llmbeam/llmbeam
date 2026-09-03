// Package connect implements the native LAN connector client.
package connect

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultHTTPTimeout = 10 * time.Second

// Session is the credential returned after a successful one-time pairing.
type Session struct {
	Token    string    `json:"token"`
	DeviceID string    `json:"device_id"`
	Expires  time.Time `json:"expires_at"`
}

// Client talks to a paired LLMBeam host.
type Client struct {
	baseURL string
	http    *http.Client
	session Session
}

// New creates a client for a host URL.
func New(baseURL string, httpClient *http.Client) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("host must be an HTTP(S) URL without a path")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	return &Client{baseURL: baseURL, http: httpClient}, nil
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
	c.session = session
	return session, nil
}

// Token returns the current connector token.
func (c *Client) Token() string { return c.session.Token }

// BaseURL returns the paired host URL.
func (c *Client) BaseURL() string { return c.baseURL }

func randomID(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate connector identity: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
