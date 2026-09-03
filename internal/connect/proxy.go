package connect

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// Listen starts a loopback-only OpenAI-compatible proxy. Only the two
// supported OpenAI paths are forwarded to the paired host.
func (c *Client) Listen(ctx context.Context, address string) (net.Listener, error) {
	listener, _, err := c.ListenWithAPIKey(ctx, address)
	return listener, err
}

// ListenWithAPIKey starts the local proxy and returns its ephemeral API key.
func (c *Client) ListenWithAPIKey(ctx context.Context, address string) (net.Listener, string, error) {
	if ctx == nil {
		return nil, "", errors.New("nil proxy context")
	}
	if c.session.Token == "" {
		return nil, "", errors.New("client is not paired")
	}
	listenAddress, err := validateListenAddress(address)
	if err != nil {
		return nil, "", err
	}
	remoteURL, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, "", err
	}
	handler := c.proxyHandler(remoteURL)
	listener, err := net.ListenTCP("tcp", listenAddress)
	if err != nil {
		return nil, "", fmt.Errorf("listen local connector: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		_ = server.Serve(listener)
	}()
	return listener, c.localAPIKey, nil
}

func (c *Client) proxyHandler(remoteURL *url.URL) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(remoteURL)
	proxy.FlushInterval = -1
	proxy.Transport = proxyTransport(c.http)
	proxy.Director = func(request *http.Request) {
		request.URL.Scheme = remoteURL.Scheme
		request.URL.Host = remoteURL.Host
		request.Host = remoteURL.Host
		request.Header.Set("Authorization", "Bearer "+c.session.Token)
	}
	proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, err error) {
		http.Error(writer, "connector upstream unavailable: "+err.Error(), http.StatusBadGateway)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !authorizedLocalRequest(request, c.localAPIKey) {
			openAIProxyError(writer, http.StatusUnauthorized, "invalid local API key")
			return
		}
		if (request.URL.Path != "/v1/models" || request.Method != http.MethodGet) &&
			(request.URL.Path != "/v1/chat/completions" || request.Method != http.MethodPost) {
			http.NotFound(writer, request)
			return
		}
		proxy.ServeHTTP(writer, request)
	})
	return handler
}

func proxyTransport(client *http.Client) http.RoundTripper {
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if base, ok := transport.(*http.Transport); ok {
		clone := base.Clone()
		if clone.ResponseHeaderTimeout == 0 {
			clone.ResponseHeaderTimeout = 30 * time.Second
		}
		return clone
	}
	return transport
}

func validateListenAddress(address string) (*net.TCPAddr, error) {
	listenAddress, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address: %w", err)
	}
	if listenAddress.IP == nil || listenAddress.IP.To4() == nil || !listenAddress.IP.IsLoopback() {
		return nil, errors.New("local connector must listen on IPv4 loopback 127.0.0.1")
	}
	return listenAddress, nil
}

func authorizedLocalRequest(request *http.Request, expected string) bool {
	const prefix = "Bearer "
	header := request.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	provided := strings.TrimSpace(header[len(prefix):])
	return provided != "" && subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func openAIProxyError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = fmt.Fprintf(writer, `{"error":{"message":%q,"type":"llmbeam_error","code":"unauthenticated"}}`, message)
}

// NormalizeHost accepts host:port or an absolute HTTP(S) URL.
func NormalizeHost(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("host is required")
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("host must be an HTTP(S) URL without a path")
	}
	return strings.TrimRight(value, "/"), nil
}
