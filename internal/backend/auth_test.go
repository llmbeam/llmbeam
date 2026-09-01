package backend

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestBackendAPIKeyEnvironmentNames(t *testing.T) {
	tests := []struct {
		backendID string
		want      []string
	}{
		{backendID: "ollama", want: []string{"SCANCHAT_OLLAMA_API_KEY"}},
		{backendID: "lm-studio", want: []string{"SCANCHAT_LM_STUDIO_API_KEY"}},
		{backendID: "llama.cpp", want: []string{"SCANCHAT_LLAMA_CPP_API_KEY", "LLAMA_ARG_API_KEY"}},
		{backendID: "omlx", want: []string{"SCANCHAT_OMLX_API_KEY", "OMLX_API_KEY"}},
		{backendID: "custom-1", want: []string{"SCANCHAT_CUSTOM_1_API_KEY"}},
	}
	for _, test := range tests {
		t.Run(test.backendID, func(t *testing.T) {
			got := backendAPIKeyEnvironment(test.backendID)
			if len(got) != len(test.want) {
				t.Fatalf("environment names = %v, want %v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("environment names = %v, want %v", got, test.want)
				}
			}
		})
	}
}

func TestLoadBackendAPIKeyUsesScanchatOverrideThenNative(t *testing.T) {
	homeDir := func() (string, error) { return "", errors.New("unused") }
	values := map[string]string{
		"SCANCHAT_LLAMA_CPP_API_KEY": "scanchat-key",
		"LLAMA_ARG_API_KEY":          "native-key",
	}
	getenv := func(name string) string { return values[name] }
	if got := loadBackendAPIKey("llama.cpp", getenv, homeDir, "linux"); got != "scanchat-key" {
		t.Fatalf("scanchat override = %q", got)
	}
	delete(values, "SCANCHAT_LLAMA_CPP_API_KEY")
	if got := loadBackendAPIKey("llama.cpp", getenv, homeDir, "linux"); got != "native-key" {
		t.Fatalf("native fallback = %q", got)
	}
}

func TestModelsDoesNotRetryUnchangedCredential(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	item := &Backend{
		ID:           "custom-1",
		BaseURL:      server.URL,
		apiKey:       "same-key",
		apiKeyLoader: func() string { return "same-key" },
	}
	if _, err := item.Models(500 * time.Millisecond); err == nil {
		t.Fatal("Models() should reject unauthorized response")
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want no retry for unchanged key", requests.Load())
	}
}
