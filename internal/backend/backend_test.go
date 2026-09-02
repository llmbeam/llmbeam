package backend

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
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

func TestProbeDoesNotFollowRedirects(t *testing.T) {
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	item := &Backend{ID: "redirect", BaseURL: redirect.URL}
	if _, err := item.Models(500 * time.Millisecond); err == nil {
		t.Fatal("Models() should reject redirects")
	}
	if followed.Load() {
		t.Fatal("Models() followed redirect outside the fixed models path")
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

func TestDefaultCandidatesCoverSupportedBackends(t *testing.T) {
	want := []string{
		"ollama", "lm-studio", "llama.cpp", "jan", "litellm", "koboldcpp",
		"gpt4all", "xinference", "lmdeploy", "sglang", "localai", "llamafile",
		"tgi", "vllm", "mlc-llm", "tensorrt-llm",
	}
	for _, goos := range []string{"linux", "windows", "darwin"} {
		candidates := defaultCandidates(goos)
		ids := make(map[string]bool)
		for _, candidate := range candidates {
			ids[candidate.ID] = true
			for _, authID := range candidate.authIDs {
				ids[authID] = true
			}
		}
		for _, id := range want {
			if !ids[id] {
				t.Errorf("%s candidates do not include %s", goos, id)
			}
		}
	}
}

func TestDefaultCandidatesMergePortAliases(t *testing.T) {
	candidates := defaultCandidates("linux")
	var port8080 Candidate
	for _, candidate := range candidates {
		if strings.HasSuffix(candidate.BaseURL, ":8080/v1") {
			port8080 = candidate
			break
		}
	}
	if port8080.ID != "llama.cpp" {
		t.Fatalf("8080 primary candidate = %+v", port8080)
	}
	for _, id := range []string{"llama.cpp", "localai", "llamafile", "tgi"} {
		if !containsString(port8080.authIDs, id) {
			t.Fatalf("8080 candidate aliases = %v, missing %s", port8080.authIDs, id)
		}
	}
}

func TestProbeCandidateUsesMatchingAliasCredential(t *testing.T) {
	t.Setenv("LLMBEAM_OMLX_API_KEY", "")
	t.Setenv("LLMBEAM_VLLM_API_KEY", "vllm-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer vllm-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"model"}]}`))
	}))
	t.Cleanup(server.Close)

	candidate := Candidate{
		ID:      "omlx",
		BaseURL: server.URL,
		authIDs: []string{"omlx", "vllm"},
	}
	matched, models := probeCandidate(candidate, 500*time.Millisecond)
	if matched.ID != "vllm" || matched.authID != "vllm" || len(models) != 1 {
		t.Fatalf("matched candidate = %+v, models = %v", matched, models)
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
	originalScan := scanLoopback
	wellKnown = []Candidate{{ID: "offline", BaseURL: "http://127.0.0.1:1/v1", Loopback: true}}
	scanLoopback = func(map[int]struct{}, time.Duration) []ProbeResult { return nil }
	t.Cleanup(func() {
		wellKnown = original
		scanLoopback = originalScan
	})

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

func TestScanLoopbackFindsOpenAICompatibleService(t *testing.T) {
	server := fakeOpenAI(t, "m1", "m2")
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fmt.Sprintf("LLMBEAM_LOCAL_%d_API_KEY", port), "")

	results := scanLoopbackRange(port, port, 1, nil, 500*time.Millisecond)
	if len(results) != 1 {
		t.Fatalf("scanLoopbackRange() = %+v", results)
	}
	wantID := fmt.Sprintf("local-%d", port)
	if result := results[0]; result.Candidate.ID != wantID ||
		result.Candidate.BaseURL != server.URL+"/v1" || result.ModelCount != 2 {
		t.Fatalf("scan result = %+v", result)
	}
}

func TestScanLoopbackRejectsNonOpenAIService(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fmt.Sprintf("LLMBEAM_LOCAL_%d_API_KEY", port), "")

	if results := scanLoopbackRange(port, port, 1, nil, 500*time.Millisecond); len(results) != 0 {
		t.Fatalf("non-OpenAI service matched: %+v", results)
	}
}

func TestScanLoopbackUsesConfiguredFrameworkCredential(t *testing.T) {
	for _, name := range []string{
		"LLMBEAM_OLLAMA_API_KEY",
		"LLMBEAM_LM_STUDIO_API_KEY",
		"LLMBEAM_LLAMA_CPP_API_KEY",
		"LLAMA_ARG_API_KEY",
		"LLMBEAM_OMLX_API_KEY",
		"OMLX_API_KEY",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("LLMBEAM_OMLX_API_KEY", "scan-key")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer scan-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fmt.Sprintf("LLMBEAM_LOCAL_%d_API_KEY", port), "")

	results := scanLoopbackRange(port, port, 1, nil, 500*time.Millisecond)
	if len(results) != 1 {
		t.Fatalf("authenticated scan = %+v", results)
	}
	wantID := fmt.Sprintf("omlx-%d", port)
	if results[0].Candidate.ID != wantID || results[0].Candidate.authID != "omlx" {
		t.Fatalf("authenticated candidate = %+v", results[0].Candidate)
	}
	models, err := backendForCandidate(results[0].Candidate).Models(500 * time.Millisecond)
	if err != nil || len(models) != 1 || models[0] != "m1" {
		t.Fatalf("registered backend models = %v, %v", models, err)
	}
}

func TestScanDoesNotSendFrameworkCredentialWithoutUnauthorized(t *testing.T) {
	t.Setenv("LLMBEAM_OMLX_API_KEY", "must-not-leak")
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Errorf("unexpected Authorization header %q", authorization)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(fmt.Sprintf("LLMBEAM_LOCAL_%d_API_KEY", port), "")

	if results := scanLoopbackRange(port, port, 1, nil, 500*time.Millisecond); len(results) != 0 {
		t.Fatalf("failed service matched: %+v", results)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one unauthenticated probe", requests.Load())
	}
}

func TestOpenLoopbackPortsHonorsExclusions(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port

	ports := openLoopbackPorts(port, port, 1, nil, 100*time.Millisecond)
	if len(ports) != 1 || ports[0] != port {
		t.Fatalf("openLoopbackPorts() = %v", ports)
	}
	ports = openLoopbackPorts(port, port, 1, map[int]struct{}{port: {}}, 100*time.Millisecond)
	if len(ports) != 0 {
		t.Fatalf("excluded ports = %v", ports)
	}
}
