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

func newConnectorTestServer(t *testing.T, backends []*backend.Backend) (*httptest.Server, *pair.Manager, *pair.ConnectorManager) {
	t.Helper()
	pairs := pair.NewManager(time.Minute)
	connectors := pair.NewConnectorManager()
	registry := backend.NewRegistry(backends, 500*time.Millisecond)
	limiter := pair.NewRateLimiter(5, time.Minute, 5*time.Minute)
	server := httptest.NewServer(NewWithConnector(pairs, registry, limiter, nil, connectors).Handler())
	t.Cleanup(server.Close)
	return server, pairs, connectors
}

func connectorTokenFromManager(t *testing.T, connectors *pair.ConnectorManager) string {
	t.Helper()
	session, ok := connectors.Redeem(connectors.Code(), "test-client", "test-public-key", "LLMBeam connector", "127.0.0.1")
	if !ok {
		t.Fatal("create connector session")
	}
	return session.Token
}

func connectorRequest(t *testing.T, method, url, token, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestOpenAIModelsRequireConnectorBearerToken(t *testing.T) {
	server, pairs, _ := newConnectorTestServer(t, nil)
	cookie, _ := pairUp(t, server, pairs, "browser")

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("cookie-only status = %d, want 401", response.StatusCode)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "unauthenticated" {
		t.Fatalf("OpenAI error = %+v", payload)
	}
}

func TestOpenAIModelsReturnsNamespacedModels(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"org/model-7b"},{"id":"model-2"}]}`))
	}))
	t.Cleanup(upstream.Close)
	server, _, connectors := newConnectorTestServer(t,
		[]*backend.Backend{{ID: "fake", BaseURL: upstream.URL + "/v1"}})
	token := connectorTokenFromManager(t, connectors)
	request := connectorRequest(t, http.MethodGet, server.URL+"/v1/models", token, "")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, body = %s", response.StatusCode, body)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Object != "list" || len(payload.Data) != 2 ||
		payload.Data[0].ID != "fake/org/model-7b" || payload.Data[0].Object != "model" ||
		payload.Data[0].OwnedBy != "llmbeam" || payload.Data[1].ID != "fake/model-2" {
		t.Fatalf("models response = %+v", payload)
	}
}

func TestOpenAIChatMapsModelAndStreamsSSE(t *testing.T) {
	received := make(chan map[string]any, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"org/model-7b"}]}`))
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- payload
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)

	server, _, connectors := newConnectorTestServer(t,
		[]*backend.Backend{{ID: "fake", BaseURL: upstream.URL + "/v1"}})
	token := connectorTokenFromManager(t, connectors)
	request := connectorRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", token,
		`{"model":"fake/org/model-7b","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status=%d content-type=%q body=%s", response.StatusCode, response.Header.Get("Content-Type"), body)
	}
	if !strings.Contains(string(body), "hello") || !strings.Contains(string(body), "[DONE]") {
		t.Fatalf("SSE body = %q", body)
	}
	select {
	case payload := <-received:
		if payload["model"] != "org/model-7b" || payload["stream"] != true {
			t.Fatalf("upstream payload = %+v", payload)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream did not receive chat request")
	}
}

func TestOpenAIChatUsesOpenAIErrorShape(t *testing.T) {
	server, _, connectors := newConnectorTestServer(t, nil)
	token := connectorTokenFromManager(t, connectors)
	request := connectorRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", token,
		`{"model":"missing/model","messages":[]}`)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Message != "unknown_model" || payload.Error.Type != "llmbeam_error" ||
		payload.Error.Code != "unknown_model" {
		t.Fatalf("error response = %+v", payload)
	}
}
