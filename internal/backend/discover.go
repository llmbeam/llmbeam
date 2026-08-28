package backend

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Candidate describes an endpoint that should be probed.
type Candidate struct {
	ID       string
	BaseURL  string
	Loopback bool
}

var wellKnown = []Candidate{
	{ID: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Loopback: true},
	{ID: "lm-studio", BaseURL: "http://127.0.0.1:1234/v1", Loopback: true},
	{ID: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1", Loopback: true},
}

// ProbeResult captures the startup status of one candidate.
type ProbeResult struct {
	Candidate  Candidate
	Up         bool
	ModelCount int
}

// Discover validates and probes well-known endpoints plus user-supplied
// OpenAI-compatible base URLs. It returns every candidate backend, including
// those currently offline, so Registry can discover them on later refreshes.
func Discover(extras []string, timeout time.Duration) ([]ProbeResult, []*Backend, error) {
	candidates := append([]Candidate(nil), wellKnown...)
	for index, raw := range extras {
		baseURL, loopback, err := normalizeBaseURL(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid --backend URL %q: %w", raw, err)
		}
		candidates = append(candidates, Candidate{
			ID:       fmt.Sprintf("custom-%d", index+1),
			BaseURL:  baseURL,
			Loopback: loopback,
		})
	}

	backends := make([]*Backend, len(candidates))
	results := make([]ProbeResult, len(candidates))
	var wait sync.WaitGroup
	for index, candidate := range candidates {
		item := &Backend{ID: candidate.ID, BaseURL: candidate.BaseURL}
		backends[index] = item
		wait.Add(1)
		go func() {
			defer wait.Done()
			models, err := item.Models(timeout)
			results[index] = ProbeResult{
				Candidate:  candidate,
				Up:         err == nil,
				ModelCount: len(models),
			}
		}()
	}
	wait.Wait()
	return results, backends, nil
}

func normalizeBaseURL(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("scheme must be http or https")
	}
	if parsed.Host == "" {
		return "", false, fmt.Errorf("host is required")
	}
	if parsed.User != nil {
		return "", false, fmt.Errorf("credentials in URL are not supported")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false, fmt.Errorf("query and fragment are not allowed")
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "" {
		parsed.Path = "/v1"
	}
	baseURL := parsed.String()
	return baseURL, isLoopbackHost(parsed.Hostname()), nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
