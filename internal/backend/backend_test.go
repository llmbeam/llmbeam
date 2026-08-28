package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeOpenAI(t *testing.T, models ...string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", func(w http.ResponseWriter, _ *http.Request) {
		data := make([]map[string]string, 0, len(models))
		for _, model := range models {
			data = append(data, map[string]string{"id": model})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestProbeUpBackend(t *testing.T) {
	server := fakeOpenAI(t, "llama3.2:3b", "qwen2.5", "qwen2.5", "")
	item := &Backend{ID: "test", BaseURL: server.URL + "/v1"}
	models, err := item.Models(500 * time.Millisecond)
	if err != nil {
		t.Fatalf("Models() error: %v", err)
	}
	if len(models) != 2 || models[0] != "llama3.2:3b" || models[1] != "qwen2.5" {
		t.Fatalf("Models() = %v", models)
	}
}

func TestProbeRejectsBadResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
	}{
		{name: "status", body: `{}`, code: http.StatusUnauthorized},
		{name: "malformed JSON", body: `{`, code: http.StatusOK},
		{name: "missing data", body: `{}`, code: http.StatusOK},
		{name: "trailing data", body: `{"data":[]} {}`, code: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.code)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			item := &Backend{ID: "bad", BaseURL: server.URL}
			if _, err := item.Models(500 * time.Millisecond); err == nil {
				t.Fatal("Models() should reject invalid response")
			}
		})
	}
}

func TestProbeDownBackend(t *testing.T) {
	item := &Backend{ID: "down", BaseURL: "http://127.0.0.1:1/v1"}
	if _, err := item.Models(200 * time.Millisecond); err == nil {
		t.Fatal("Models() should fail for an unreachable backend")
	}
}

func TestRegistryListCacheAndResolve(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"data":[{"id":"org/model-7b"}]}`))
	}))
	t.Cleanup(server.Close)

	registry := NewRegistry([]*Backend{{ID: "fake", BaseURL: server.URL}}, 500*time.Millisecond)
	first := registry.ListModels()
	second := registry.ListModels()
	if len(first) != 1 || first[0].ID != "fake/org/model-7b" || len(second) != 1 {
		t.Fatalf("ListModels() = %+v then %+v", first, second)
	}
	if requests.Load() != 1 {
		t.Fatalf("model endpoint requests = %d, want 1 cached request", requests.Load())
	}

	item, model, ok := registry.Resolve("fake/org/model-7b")
	if !ok || item.ID != "fake" || model != "org/model-7b" {
		t.Fatalf("Resolve() = %+v, %q, %v", item, model, ok)
	}
	if _, _, ok := registry.Resolve("missing/model"); ok {
		t.Fatal("unknown backend must not resolve")
	}
	if _, _, ok := registry.Resolve("missing-separator"); ok {
		t.Fatal("malformed model ID must not resolve")
	}
}

func TestRegistryRefreshFindsBackendThatComesOnline(t *testing.T) {
	clock := time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)
	up := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !up.Load() {
			http.Error(w, "offline", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(server.Close)

	registry := newRegistry(
		[]*Backend{{ID: "late", BaseURL: server.URL}},
		500*time.Millisecond,
		30*time.Second,
		func() time.Time { return clock },
	)
	if models := registry.ListModels(); len(models) != 0 {
		t.Fatalf("offline backend returned models: %+v", models)
	}

	up.Store(true)
	clock = clock.Add(30 * time.Second)
	models := registry.ListModels()
	if len(models) != 1 || models[0].ID != "late/m1" {
		t.Fatalf("refreshed models = %+v", models)
	}
}

func TestRegistryCachesEmptyResult(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "offline", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	registry := NewRegistry([]*Backend{{ID: "offline", BaseURL: server.URL}}, 500*time.Millisecond)
	if models := registry.ListModels(); len(models) != 0 {
		t.Fatalf("offline models = %+v", models)
	}
	if models := registry.ListModels(); len(models) != 0 {
		t.Fatalf("cached offline models = %+v", models)
	}
	if requests.Load() != 1 {
		t.Fatalf("offline endpoint requests = %d, want 1 cached request", requests.Load())
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	tests := []struct {
		raw          string
		want         string
		wantLoopback bool
	}{
		{raw: "http://localhost:8000", want: "http://localhost:8000/v1", wantLoopback: true},
		{raw: "http://127.0.0.1:8000/v1/", want: "http://127.0.0.1:8000/v1", wantLoopback: true},
		{raw: "https://models.example/api/v1", want: "https://models.example/api/v1", wantLoopback: false},
	}
	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			got, loopback, err := normalizeBaseURL(tt.raw)
			if err != nil || got != tt.want || loopback != tt.wantLoopback {
				t.Fatalf("normalizeBaseURL() = %q, %v, %v", got, loopback, err)
			}
		})
	}

	for _, raw := range []string{
		"localhost:8000",
		"ftp://localhost/models",
		"http://localhost/v1?token=x",
		"http://user:secret@localhost/v1",
	} {
		t.Run("invalid "+raw, func(t *testing.T) {
			if _, _, err := normalizeBaseURL(raw); err == nil {
				t.Fatalf("normalizeBaseURL(%q) should fail", raw)
			}
		})
	}
}

func TestDefaultCandidatesIncludeOMLXOnlyOnMacOS(t *testing.T) {
	darwin := defaultCandidates("darwin")
	omlx, ok := findCandidate(darwin, "omlx")
	if !ok {
		t.Fatal("Darwin candidates must include oMLX")
	}
	if omlx.BaseURL != "http://127.0.0.1:8000/v1" || !omlx.Loopback {
		t.Fatalf("oMLX candidate = %+v", omlx)
	}

	for _, goos := range []string{"linux", "windows"} {
		if _, ok := findCandidate(defaultCandidates(goos), "omlx"); ok {
			t.Fatalf("%s candidates must not auto-detect macOS-only oMLX", goos)
		}
	}
}

func findCandidate(candidates []Candidate, id string) (Candidate, bool) {
	for _, candidate := range candidates {
		if candidate.ID == id {
			return candidate, true
		}
	}
	return Candidate{}, false
}

func TestDiscoverKeepsOfflineCandidates(t *testing.T) {
	original := wellKnown
	wellKnown = []Candidate{{ID: "offline", BaseURL: "http://127.0.0.1:1/v1", Loopback: true}}
	t.Cleanup(func() { wellKnown = original })

	server := fakeOpenAI(t, "m1")
	results, backends, err := Discover([]string{strings.TrimSuffix(server.URL, "/")}, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Discover() error: %v", err)
	}
	if len(results) != 2 || results[0].Up || !results[1].Up || results[1].ModelCount != 1 {
		t.Fatalf("Discover() results = %+v", results)
	}
	if len(backends) != 2 || backends[0].ID != "offline" || backends[1].BaseURL != server.URL+"/v1" {
		t.Fatalf("Discover() backends = %+v", backends)
	}
}
