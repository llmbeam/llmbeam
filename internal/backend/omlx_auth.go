package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type credentialLoader func() string

type omlxSettings struct {
	Auth struct {
		APIKey string `json:"api_key"`
	} `json:"auth"`
}

func omlxCredentials() (string, credentialLoader) {
	load := func() string { return loadOMLXAPIKey(os.Getenv, os.UserHomeDir, runtime.GOOS) }
	return load(), load
}

func loadOMLXAPIKey(getenv func(string) string, homeDir func() (string, error), goos string) string {
	if key := validAPIKey(getenv("SCANCHAT_OMLX_API_KEY")); key != "" {
		return key
	}
	if key := validAPIKey(getenv("OMLX_API_KEY")); key != "" {
		return key
	}

	home, err := homeDir()
	if err != nil {
		return ""
	}
	basePath := strings.TrimSpace(getenv("OMLX_BASE_PATH"))
	if basePath == "" && goos == "darwin" {
		bootstrap := filepath.Join(home, "Library", "Application Support", "oMLX", "base-path")
		if data, readErr := os.ReadFile(bootstrap); readErr == nil {
			basePath = strings.TrimSpace(string(data))
		}
	}
	if basePath == "" {
		basePath = filepath.Join(home, ".omlx")
	}
	return readOMLXSettings(filepath.Join(basePath, "settings.json"))
}

func readOMLXSettings(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return ""
	}
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	var settings omlxSettings
	if err := json.NewDecoder(file).Decode(&settings); err != nil {
		return ""
	}
	return validAPIKey(settings.Auth.APIKey)
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

func (b *Backend) ApplyAuth(request *http.Request) {
	key := b.currentAPIKey()
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
}

// RefreshAuth reloads credentials from the configured source, if any. It is
// used after an upstream reports that the current credential is unauthorized.
func (b *Backend) RefreshAuth() bool {
	_, loader := b.authState()
	if loader == nil {
		return false
	}
	b.refreshAPIKey()
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
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		return false
	}
	if !b.RefreshAuth() {
		return false
	}
	_ = response.Body.Close()
	return true
}

func (b *Backend) authState() (string, credentialLoader) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.apiKey, b.apiKeyLoader
}
