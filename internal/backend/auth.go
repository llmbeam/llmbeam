package backend

import (
	"net/http"
	"os"
	"runtime"
	"strings"
)

type credentialLoader func() string

func backendCredentials(backendID string) (string, credentialLoader) {
	load := func() string {
		return loadBackendAPIKey(backendID, os.Getenv, os.UserHomeDir, runtime.GOOS)
	}
	return load(), load
}

func loadBackendAPIKey(
	backendID string,
	getenv func(string) string,
	homeDir func() (string, error),
	goos string,
) string {
	for _, name := range backendAPIKeyEnvironment(backendID) {
		if key := validAPIKey(getenv(name)); key != "" {
			return key
		}
	}
	if backendID == "omlx" {
		return loadOMLXSettingsAPIKey(getenv, homeDir, goos)
	}
	return ""
}

func backendAPIKeyEnvironment(backendID string) []string {
	scanchatName := "SCANCHAT_" + strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToUpper(backendID)) + "_API_KEY"
	names := []string{scanchatName}
	switch backendID {
	case "llama.cpp":
		names = append(names, "LLAMA_ARG_API_KEY")
	case "omlx":
		names = append(names, "OMLX_API_KEY")
	}
	return names
}

func validAPIKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t\r\n") || !isASCII(key) {
		return ""
	}
	return key
}

func isASCII(value string) bool {
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

// ApplyAuth adds the backend's current bearer credential to an upstream request.
func (b *Backend) ApplyAuth(request *http.Request) {
	key := b.currentAPIKey()
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
}

// RefreshAuth reloads credentials and reports whether they changed. Callers
// can safely retry a 401 only when a new credential became available.
func (b *Backend) RefreshAuth() bool {
	b.mu.RLock()
	oldKey, loader := b.apiKey, b.apiKeyLoader
	b.mu.RUnlock()
	if loader == nil {
		return false
	}
	newKey := loader()
	if newKey == oldKey {
		return false
	}
	b.mu.Lock()
	b.apiKey = newKey
	b.mu.Unlock()
	return true
}

func (b *Backend) currentAPIKey() string {
	b.mu.RLock()
	key, loader := b.apiKey, b.apiKeyLoader
	b.mu.RUnlock()
	if key != "" || loader == nil {
		return key
	}
	return b.refreshAPIKey()
}

func (b *Backend) refreshAPIKey() string {
	b.mu.RLock()
	loader := b.apiKeyLoader
	b.mu.RUnlock()
	if loader == nil {
		return ""
	}
	key := loader()
	b.mu.Lock()
	b.apiKey = key
	b.mu.Unlock()
	return key
}

func (b *Backend) retryAuthAfterUnauthorized(response *http.Response) bool {
	if response == nil || response.StatusCode != http.StatusUnauthorized || !b.RefreshAuth() {
		return false
	}
	_ = response.Body.Close()
	return true
}
