// Package backend discovers and represents OpenAI-compatible upstreams.
package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	cacheTTL         = 30 * time.Second
	maxModelsPayload = 4 << 20
)

// Backend is an OpenAI-compatible API base.
type Backend struct {
	ID      string
	BaseURL string
}

// ModelInfo is the gateway-facing identity of an upstream model.
type ModelInfo struct {
	ID      string `json:"id"`
	Backend string `json:"backend"`
	Name    string `json:"name"`
}

// Models fetches GET <base>/models and returns the advertised model IDs.
func (b *Backend) Models(timeout time.Duration) ([]string, error) {
	request, err := http.NewRequest(http.MethodGet, b.BaseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build %s models request: %w", b.ID, err)
	}
	request.Header.Set("Accept", "application/json")

	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("query %s models: %w", b.ID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return nil, fmt.Errorf("query %s models: status %d", b.ID, response.StatusCode)
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxModelsPayload+1))
	if err != nil {
		return nil, fmt.Errorf("read %s models: %w", b.ID, err)
	}
	if len(body) > maxModelsPayload {
		return nil, fmt.Errorf("decode %s models: response exceeds %d bytes", b.ID, maxModelsPayload)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode %s models: %w", b.ID, err)
	}
	if payload.Data == nil {
		return nil, fmt.Errorf("decode %s models: missing data array", b.ID)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("decode %s models: trailing JSON data", b.ID)
	}

	models := make([]string, 0, len(payload.Data))
	seen := make(map[string]struct{}, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID == "" {
			continue
		}
		if _, duplicate := seen[item.ID]; duplicate {
			continue
		}
		seen[item.ID] = struct{}{}
		models = append(models, item.ID)
	}
	return models, nil
}

// Registry provides cached model listing and namespaced model resolution.
type Registry struct {
	mu         sync.RWMutex
	refreshMu  sync.Mutex
	backends   []*Backend
	timeout    time.Duration
	cache      []ModelInfo
	cachedAt   time.Time
	cacheReady bool
	now        func() time.Time
	cacheTTL   time.Duration
}

// NewRegistry creates a registry containing every candidate backend. Offline
// candidates remain registered so a later model refresh can discover them.
func NewRegistry(backends []*Backend, timeout time.Duration) *Registry {
	return newRegistry(backends, timeout, cacheTTL, time.Now)
}

func newRegistry(backends []*Backend, timeout, ttl time.Duration, now func() time.Time) *Registry {
	return &Registry{
		backends: append([]*Backend(nil), backends...),
		timeout:  timeout,
		now:      now,
		cacheTTL: ttl,
	}
}

// ListModels probes all backends when the cache is stale and returns
// namespaced model IDs. Backend requests run concurrently and never hold mu.
func (r *Registry) ListModels() []ModelInfo {
	if models, fresh := r.cachedModels(); fresh {
		return models
	}

	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if models, fresh := r.cachedModels(); fresh {
		return models
	}

	backends := r.Backends()
	results := make([][]string, len(backends))
	var wait sync.WaitGroup
	for index, item := range backends {
		wait.Add(1)
		go func() {
			defer wait.Done()
			models, err := item.Models(r.timeout)
			if err == nil {
				results[index] = models
			}
		}()
	}
	wait.Wait()

	models := make([]ModelInfo, 0)
	for index, item := range backends {
		for _, model := range results[index] {
			models = append(models, ModelInfo{
				ID:      item.ID + "/" + model,
				Backend: item.ID,
				Name:    model,
			})
		}
	}

	r.mu.Lock()
	r.cache = append([]ModelInfo(nil), models...)
	r.cachedAt = r.now()
	r.cacheReady = true
	r.mu.Unlock()
	return append([]ModelInfo(nil), models...)
}

func (r *Registry) cachedModels() ([]ModelInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fresh := r.cacheReady && r.now().Sub(r.cachedAt) < r.cacheTTL
	return append([]ModelInfo(nil), r.cache...), fresh
}

// Resolve splits a namespaced model ID on its first slash and returns its
// registered backend and original upstream model ID.
func (r *Registry) Resolve(namespaced string) (*Backend, string, bool) {
	backendID, model, found := strings.Cut(namespaced, "/")
	if !found || backendID == "" || model == "" {
		return nil, "", false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, item := range r.backends {
		if item.ID == backendID {
			return item, model, true
		}
	}
	return nil, "", false
}

// Backends returns a snapshot of the configured backend list.
func (r *Registry) Backends() []*Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*Backend(nil), r.backends...)
}
