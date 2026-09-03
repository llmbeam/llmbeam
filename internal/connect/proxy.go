package connect

import (
	"context"
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
	if ctx == nil {
		return nil, errors.New("nil proxy context")
	}
	if c.session.Token == "" {
		return nil, errors.New("client is not paired")
	}
	listenAddress, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("invalid listen address: %w", err)
	}
	if !listenAddress.IP.IsLoopback() {
		return nil, errors.New("local connector must listen on a loopback address")
	}
	remoteURL, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(remoteURL)
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
		if request.URL.Path != "/v1/models" && request.URL.Path != "/v1/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		proxy.ServeHTTP(writer, request)
	})
	listener, err := net.ListenTCP("tcp", listenAddress)
	if err != nil {
		return nil, fmt.Errorf("listen local connector: %w", err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Shutdown(context.Background())
	}()
	go func() {
		_ = server.Serve(listener)
	}()
	return listener, nil
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
