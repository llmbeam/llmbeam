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

func TestModels(t *testing.T) {
	client, err := New("http://llmbeam.test:8442", &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			return nil, fmt.Errorf("unexpected models request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer connector-token" {
			return nil, fmt.Errorf("unexpected authorization header: %q", request.Header.Get("Authorization"))
		}
		body := `{"object":"list","data":[{"id":"ollama/llama3.2","object":"model","owned_by":"llmbeam"},{"id":"llama.cpp/Qwen3-8B"}]}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	client.session.Token = "connector-token"
	models, err := client.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "ollama/llama3.2" || models[1].ID != "llama.cpp/Qwen3-8B" {
		t.Fatalf("Models() = %#v", models)
	}
}

func TestModelsErrors(t *testing.T) {
	tests := []struct {
		name       string
		response   *http.Response
		requestErr error
		contains   string
	}{
		{name: "unauthorized", response: &http.Response{StatusCode: http.StatusUnauthorized, Status: "401 Unauthorized", Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, contains: "invalid or expired"},
		{name: "server error", response: &http.Response{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, contains: "500 Internal Server Error"},
		{name: "malformed json", response: &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`not-json`)), Header: make(http.Header)}, contains: "decode models response"},
		{name: "transport error", requestErr: fmt.Errorf("connection reset"), contains: "retrieve models"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New("http://llmbeam.test:8442", &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
				return test.response, test.requestErr
			})})
			if err != nil {
				t.Fatal(err)
			}
			client.session.Token = "connector-token"
			_, err = client.Models(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("Models() error = %v, want substring %q", err, test.contains)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
