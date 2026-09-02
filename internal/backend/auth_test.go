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
		{backendID: "ollama", want: []string{"LLMBEAM_OLLAMA_API_KEY"}},
		{backendID: "lm-studio", want: []string{"LLMBEAM_LM_STUDIO_API_KEY"}},
		{backendID: "llama.cpp", want: []string{"LLMBEAM_LLAMA_CPP_API_KEY", "LLAMA_ARG_API_KEY"}},
		{backendID: "omlx", want: []string{"LLMBEAM_OMLX_API_KEY", "OMLX_API_KEY"}},
		{backendID: "vllm", want: []string{"LLMBEAM_VLLM_API_KEY", "VLLM_API_KEY"}},
		{backendID: "sglang", want: []string{"LLMBEAM_SGLANG_API_KEY", "SGLANG_API_KEY"}},
		{backendID: "localai", want: []string{"LLMBEAM_LOCALAI_API_KEY", "LOCALAI_API_KEY"}},
		{backendID: "litellm", want: []string{"LLMBEAM_LITELLM_API_KEY", "LITELLM_MASTER_KEY", "LITELLM_API_KEY"}},
		{backendID: "xinference", want: []string{"LLMBEAM_XINFERENCE_API_KEY", "XINFERENCE_API_KEY"}},
		{backendID: "lmdeploy", want: []string{"LLMBEAM_LMDEPLOY_API_KEY", "LMDEPLOY_API_KEY"}},
		{backendID: "mlx-lm", want: []string{"LLMBEAM_MLX_LM_API_KEY"}},
		{backendID: "gpt4all", want: []string{"LLMBEAM_GPT4ALL_API_KEY"}},
		{backendID: "custom-1", want: []string{"LLMBEAM_CUSTOM_1_API_KEY"}},
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

func TestLoadBackendAPIKeyUsesLLMBeamOverrideThenNative(t *testing.T) {
	homeDir := func() (string, error) { return "", errors.New("unused") }
	values := map[string]string{
		"LLMBEAM_LLAMA_CPP_API_KEY": "llmbeam-key",
		"LLAMA_ARG_API_KEY":         "native-key",
	}
	getenv := func(name string) string { return values[name] }
	if got := loadBackendAPIKey("llama.cpp", getenv, homeDir, "linux"); got != "llmbeam-key" {
		t.Fatalf("llmbeam override = %q", got)
	}
	delete(values, "LLMBEAM_LLAMA_CPP_API_KEY")
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
