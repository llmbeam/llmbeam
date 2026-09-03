package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNormalizeHost(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"192.168.1.5:8442", "http://192.168.1.5:8442"},
		{" https://example.test ", "https://example.test"},
		{"https://example.test/", "https://example.test"},
	} {
		got, err := NormalizeHost(test.input)
		if err != nil || got != test.want {
			t.Fatalf("NormalizeHost(%q) = %q, %v", test.input, got, err)
		}
	}
	for _, input := range []string{"", "example.test/path", "ftp://example.test"} {
		if _, err := NormalizeHost(input); err == nil {
			t.Fatalf("NormalizeHost(%q) unexpectedly succeeded", input)
		}
	}
}

func TestPair(t *testing.T) {
	client, err := New("http://llmbeam.test:8442", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/connector/pair" {
			return nil, fmt.Errorf("unexpected pairing request: %s %s", request.Method, request.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			return nil, err
		}
		if payload["code"] != "K7M4QX" || payload["client_id"] == "" || payload["client_public_key"] == "" {
			return nil, fmt.Errorf("unexpected pairing payload: %#v", payload)
		}
		body, _ := json.Marshal(map[string]any{"token": "connector-token", "device_id": "c_test", "expires_at": time.Now().Add(time.Hour)})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(body))), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.Pair(context.Background(), "K7M4QX")
	if err != nil || session.Token != "connector-token" || client.Token() != session.Token {
		t.Fatalf("Pair() = %#v, %v", session, err)
	}
}

func TestPairErrorsAreActionable(t *testing.T) {
	client, _ := New("http://llmbeam.test:8442", &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(`{"error":"invalid_or_expired_code"}`)), Header: make(http.Header)}, nil
	})})
	_, err := client.Pair(context.Background(), "BAD")
	if err == nil || !strings.Contains(err.Error(), "invalid or expired") {
		t.Fatalf("Pair() error = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
