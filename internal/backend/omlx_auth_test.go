package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoadOMLXAPIKeyPriority(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "omlx")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(base, "settings.json")
	file, err := os.OpenFile(settings, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(map[string]any{"auth": map[string]string{"api_key": "file-key"}}); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	getenv := func(name string) string {
		switch name {
		case "LLMBEAM_OMLX_API_KEY":
			return "llmbeam-key"
		case "OMLX_API_KEY":
			return "omlx-key"
		case "OMLX_BASE_PATH":
			return base
		default:
			return ""
		}
	}
	homeDir := func() (string, error) { return home, nil }
	if got := loadOMLXAPIKey(getenv, homeDir, "linux"); got != "llmbeam-key" {
		t.Fatalf("explicit llmbeam key = %q", got)
	}

	getenv = func(name string) string {
		if name == "OMLX_API_KEY" {
			return "omlx-key"
		}
		if name == "OMLX_BASE_PATH" {
			return base
		}
		return ""
	}
	if got := loadOMLXAPIKey(getenv, homeDir, "linux"); got != "omlx-key" {
		t.Fatalf("OMLX_API_KEY = %q", got)
	}

	getenv = func(name string) string {
		if name == "OMLX_BASE_PATH" {
			return base
		}
		return ""
	}
	if got := loadOMLXAPIKey(getenv, homeDir, "linux"); got != "file-key" {
		t.Fatalf("settings key = %q", got)
	}
}

func TestLoadOMLXAPIKeyIgnoresInsecureSettings(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, ".omlx")
	if err := os.Mkdir(base, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "settings.json")
	if err := os.WriteFile(path, []byte(`{"auth":{"api_key":"file-key"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	getenv := func(string) string { return "" }
	homeDir := func() (string, error) { return home, nil }
	if got := loadOMLXAPIKey(getenv, homeDir, "linux"); got != "" {
		t.Fatalf("insecure settings key = %q, want empty", got)
	}
}

func TestModelsSendsAndRefreshesBearerKey(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer new-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "model"}}})
	}))
	t.Cleanup(server.Close)
	item := &Backend{
		ID:           "omlx",
		BaseURL:      server.URL,
		apiKey:       "old-key",
		apiKeyLoader: func() string { return "new-key" },
	}
	models, err := item.Models(500 * time.Millisecond)
	if err != nil || len(models) != 1 || models[0] != "model" {
		t.Fatalf("Models() = %v, %v", models, err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want refresh retry", requests.Load())
	}
}

func TestDiscoverConfiguresBackendAuth(t *testing.T) {
	tests := []struct {
		backendID string
		envName   string
		custom    bool
	}{
		{backendID: "ollama", envName: "LLMBEAM_OLLAMA_API_KEY"},
		{backendID: "lm-studio", envName: "LLMBEAM_LM_STUDIO_API_KEY"},
		{backendID: "llama.cpp", envName: "LLMBEAM_LLAMA_CPP_API_KEY"},
		{backendID: "omlx", envName: "LLMBEAM_OMLX_API_KEY"},
		{backendID: "custom-1", envName: "LLMBEAM_CUSTOM_1_API_KEY", custom: true},
	}
	for _, test := range tests {
		t.Run(test.backendID, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer discover-key" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "model"}}})
			}))
			t.Cleanup(server.Close)
			t.Setenv(test.envName, "discover-key")

			original := wellKnown
			extras := []string(nil)
			if test.custom {
				wellKnown = nil
				extras = []string{server.URL}
			} else {
				wellKnown = []Candidate{{ID: test.backendID, BaseURL: server.URL, Loopback: true}}
			}
			t.Cleanup(func() { wellKnown = original })

			results, _, err := Discover(extras, 500*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || !results[0].Up || results[0].ModelCount != 1 {
				t.Fatalf("%s discovery = %+v", test.backendID, results)
			}
		})
	}
}
