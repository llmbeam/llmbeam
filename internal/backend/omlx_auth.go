package backend

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type omlxSettings struct {
	Auth struct {
		APIKey string `json:"api_key"`
	} `json:"auth"`
}

func loadOMLXAPIKey(getenv func(string) string, homeDir func() (string, error), goos string) string {
	return loadBackendAPIKey("omlx", getenv, homeDir, goos)
}

func loadOMLXSettingsAPIKey(getenv func(string) string, homeDir func() (string, error), goos string) string {
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
