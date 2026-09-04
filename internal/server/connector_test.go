package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/llmbeam/llmbeam/internal/backend"
	"github.com/llmbeam/llmbeam/internal/pair"
)

func TestConnectorInfoDoesNotExposeCode(t *testing.T) {
	server, _, connectors := newConnectorTestServer(t, nil)
	response, err := http.Get(server.URL + "/api/connector/info")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || payload["object"] != "connector_info" {
		t.Fatalf("status=%d payload=%v", response.StatusCode, payload)
	}
	if _, exists := payload["code"]; exists || strings.Contains(string(mustJSON(t, payload)), connectors.Code()) {
		t.Fatalf("connector info leaked code: %v", payload)
	}
}

func TestConnectorSessionsRedactsCredentials(t *testing.T) {
	server, _, connectors := newConnectorTestServer(t, nil)
	paired := connectorTokenFromManager(t, connectors)
	request := connectorRequest(t, http.MethodGet, server.URL+"/api/connector/sessions", paired, "")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	if strings.Contains(string(body), paired) || strings.Contains(string(body), "test-public-key") {
		t.Fatalf("session response leaked credentials: %s", body)
	}
	if !strings.Contains(string(body), "test-client") {
		t.Fatalf("session response omitted client metadata: %s", body)
	}
}

func TestConnectorSessionInvalidAfterPCARestart(t *testing.T) {
	firstConnectors := pair.NewConnectorManager()
	oldToken := connectorTokenFromManager(t, firstConnectors)

	secondConnectors := pair.NewConnectorManager()
	if _, ok := secondConnectors.Session(oldToken); ok {
		t.Fatal("connector session survived PC A restart")
	}
	registry := backend.NewRegistry(nil, time.Second)
	handler := NewWithConnector(pair.NewManager(time.Minute), registry, pair.NewRateLimiter(5, time.Minute, time.Minute), nil, secondConnectors).Handler()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/models", nil)
	request.Header.Set("Authorization", "Bearer "+oldToken)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("old connector token status=%d, want 401", response.Code)
	}
}

func TestConnectorPairRefreshAndRevoke(t *testing.T) {
	server, _, connectors := newConnectorTestServer(t, nil)
	requestBody := `{"code":"` + connectors.Code() + `","client_id":"client-a","client_public_key":"key-a"}`
	response, err := http.Post(server.URL+"/api/connector/pair", "application/json", strings.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	var paired struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&paired); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || paired.Token == "" {
		t.Fatalf("pair status=%d token=%q", response.StatusCode, paired.Token)
	}

	refresh := connectorRequest(t, http.MethodPost, server.URL+"/api/connector/refresh", paired.Token, `{}`)
	response, err = http.DefaultClient.Do(refresh)
	if err != nil {
		t.Fatal(err)
	}
	var refreshed struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&refreshed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK || refreshed.Token == "" || refreshed.Token == paired.Token {
		t.Fatalf("refresh status=%d token=%q", response.StatusCode, refreshed.Token)
	}

	revoke := connectorRequest(t, http.MethodPost, server.URL+"/api/connector/revoke", refreshed.Token, `{}`)
	response, err = http.DefaultClient.Do(revoke)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"revoked":true`) {
		t.Fatalf("revoke status=%d body=%s", response.StatusCode, body)
	}

	request := connectorRequest(t, http.MethodGet, server.URL+"/v1/models", refreshed.Token, "")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked token status=%d, want 401", response.StatusCode)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
