package connect

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testPairedClient(t *testing.T, roundTrip roundTripFunc) *Client {
	t.Helper()
	client, err := New("http://llmbeam.test:8442", &http.Client{Transport: roundTrip})
	if err != nil {
		t.Fatal(err)
	}
	client.session = Session{Token: "remote-connector-token", Expires: time.Now().Add(time.Hour)}
	return client
}

func TestProxyRequiresLocalAPIKeyAndPathAllowlist(t *testing.T) {
	called := false
	client := testPairedClient(t, func(request *http.Request) (*http.Response, error) {
		called = true
		return nil, errors.New("unexpected upstream call")
	})
	remoteURL, _ := url.Parse(client.BaseURL())
	handler := client.proxyHandler(remoteURL)

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || called {
		t.Fatalf("missing API key status=%d upstream=%v", response.Code, called)
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/admin", nil)
	request.Header.Set("Authorization", "Bearer "+client.APIKey())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("disallowed path status=%d upstream=%v", response.Code, called)
	}
}

func TestProxyInjectsConnectorTokenAndStreamsSSE(t *testing.T) {
	client := testPairedClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer remote-connector-token" {
			return nil, errors.New("connector token was not injected")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")),
		}, nil
	})
	remoteURL, _ := url.Parse(client.BaseURL())
	handler := client.proxyHandler(remoteURL)
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/chat/completions", strings.NewReader(`{"model":"fake","stream":true}`))
	request.Header.Set("Authorization", "Bearer "+client.APIKey())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("SSE response status=%d content-type=%q", response.Code, response.Header().Get("Content-Type"))
	}
	if !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("SSE body=%q", response.Body.String())
	}
}

func TestProxyPreservesUpstreamErrorsAndMapsTransportFailure(t *testing.T) {
	client := testPairedClient(t, func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"upstream unauthorized"}`))}, nil
	})
	remoteURL, _ := url.Parse(client.BaseURL())
	handler := client.proxyHandler(remoteURL)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+client.APIKey())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("upstream 401 became %d", response.Code)
	}

	client = testPairedClient(t, func(_ *http.Request) (*http.Response, error) { return nil, errors.New("connection reset") })
	remoteURL, _ = url.Parse(client.BaseURL())
	handler = client.proxyHandler(remoteURL)
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+client.APIKey())
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), "upstream unavailable") {
		t.Fatalf("transport failure status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestValidateListenAddressOnlyAllowsLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "localhost:8333"} {
		if _, err := validateListenAddress(address); err != nil {
			t.Errorf("loopback address %q rejected: %v", address, err)
		}
	}
	for _, address := range []string{"[::1]:8333", "0.0.0.0:8333", "192.168.1.2:8333", ":8333"} {
		if _, err := validateListenAddress(address); err == nil {
			t.Errorf("non-loopback address %q accepted", address)
		}
	}
}

func TestListenRejectsNilContextAndUnpairedClient(t *testing.T) {
	client := testPairedClient(t, nil)
	client.session = Session{}
	if _, _, err := client.ListenWithAPIKey(context.Background(), "127.0.0.1:0"); err == nil {
		t.Fatal("unpaired client unexpectedly listened")
	}
	if _, _, err := client.ListenWithAPIKey(nil, "127.0.0.1:0"); err == nil {
		t.Fatal("nil context unexpectedly accepted")
	}
}
