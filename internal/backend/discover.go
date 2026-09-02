package backend

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	firstScanPort   = 1
	lastScanPort    = 65535
	portScanWorkers = 128
	portDialTimeout = 25 * time.Millisecond
)

// Candidate describes an endpoint that should be probed.
type Candidate struct {
	ID           string
	BaseURL      string
	Loopback     bool
	authID       string
	authIDs      []string
	NonStreaming bool
}

var wellKnown = defaultCandidates(runtime.GOOS)

var scanLoopback = func(excluded map[int]struct{}, timeout time.Duration) []ProbeResult {
	return scanLoopbackRange(firstScanPort, lastScanPort, portScanWorkers, excluded, timeout)
}

func defaultCandidates(goos string) []Candidate {
	definitions := make([]backendSpec, 0, len(supportedBackendSpecs))
	for _, spec := range supportedBackendSpecs {
		if spec.darwinOnly && goos != "darwin" {
			continue
		}
		definitions = append(definitions, spec)
	}
	return mergeCandidates(definitions)
}

type backendSpec struct {
	id           string
	port         int
	nonStreaming bool
	darwinOnly   bool
	nativeEnv    []string
}

var supportedBackendSpecs = []backendSpec{
	{id: "ollama", port: 11434},
	{id: "lm-studio", port: 1234},
	{id: "llama.cpp", port: 8080, nativeEnv: []string{"LLAMA_ARG_API_KEY"}},
	{id: "jan", port: 1337},
	{id: "litellm", port: 4000, nativeEnv: []string{"LITELLM_MASTER_KEY", "LITELLM_API_KEY"}},
	{id: "koboldcpp", port: 5001},
	{id: "gpt4all", port: 4891, nonStreaming: true},
	{id: "xinference", port: 9997, nativeEnv: []string{"XINFERENCE_API_KEY"}},
	{id: "lmdeploy", port: 23333, nativeEnv: []string{"LMDEPLOY_API_KEY"}},
	{id: "sglang", port: 30000, nativeEnv: []string{"SGLANG_API_KEY"}},
	{id: "localai", port: 8080, nativeEnv: []string{"LOCALAI_API_KEY"}},
	{id: "llamafile", port: 8080},
	{id: "tgi", port: 8080},
	{id: "omlx", port: 8000, darwinOnly: true, nativeEnv: []string{"OMLX_API_KEY"}},
	{id: "mlx-lm", port: 8080, darwinOnly: true},
	{id: "vllm", port: 8000, nativeEnv: []string{"VLLM_API_KEY"}},
	{id: "mlc-llm", port: 8000},
	{id: "tensorrt-llm", port: 8000},
}

func mergeCandidates(definitions []backendSpec) []Candidate {
	byURL := make(map[string]int, len(definitions))
	candidates := make([]Candidate, 0, len(definitions))
	for _, definition := range definitions {
		baseURL := fmt.Sprintf("http://127.0.0.1:%d/v1", definition.port)
		candidate := Candidate{
			ID:           definition.id,
			BaseURL:      baseURL,
			Loopback:     true,
			authIDs:      []string{definition.id},
			NonStreaming: definition.nonStreaming,
		}
		if index, exists := byURL[baseURL]; exists {
			candidates[index].authIDs = append(candidates[index].authIDs, definition.id)
			continue
		}
		byURL[baseURL] = len(candidates)
		candidates = append(candidates, candidate)
	}
	return candidates
}

func knownBackendIDs() []string {
	ids := make([]string, 0, len(supportedBackendSpecs))
	for _, spec := range supportedBackendSpecs {
		ids = append(ids, spec.id)
	}
	return ids
}

func backendSpecForID(id string) (backendSpec, bool) {
	for _, spec := range supportedBackendSpecs {
		if spec.id == id {
			return spec, true
		}
	}
	return backendSpec{}, false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// ProbeResult captures the startup status of one candidate.
type ProbeResult struct {
	Candidate  Candidate
	Up         bool
	ModelCount int
}

// Discover probes well-known, scanned loopback, and user-supplied endpoints.
// Known and explicit candidates remain registered while offline so Registry
// can discover them on later refreshes.
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
	scanned := scanLoopback(loopbackPorts(candidates), timeout)

	backends := make([]*Backend, len(candidates), len(candidates)+len(scanned))
	results := make([]ProbeResult, len(candidates), len(candidates)+len(scanned))
	var wait sync.WaitGroup
	for index, candidate := range candidates {
		wait.Add(1)
		go func(index int, candidate Candidate) {
			defer wait.Done()
			matched, models := probeCandidate(candidate, timeout)
			backends[index] = backendForCandidate(matched)
			results[index] = ProbeResult{
				Candidate:  matched,
				Up:         models != nil,
				ModelCount: len(models),
			}
		}(index, candidate)
	}
	wait.Wait()
	for _, result := range scanned {
		results = append(results, result)
		backends = append(backends, backendForCandidate(result.Candidate))
	}
	return results, backends, nil
}

func loopbackPorts(candidates []Candidate) map[int]struct{} {
	ports := make(map[int]struct{})
	for _, candidate := range candidates {
		parsed, err := url.Parse(candidate.BaseURL)
		if err != nil || !isLoopbackHost(parsed.Hostname()) {
			continue
		}
		port, err := portNumber(parsed)
		if err == nil {
			ports[port] = struct{}{}
		}
	}
	return ports
}

func portNumber(parsed *url.URL) (int, error) {
	if parsed.Port() == "" {
		switch parsed.Scheme {
		case "http":
			return 80, nil
		case "https":
			return 443, nil
		}
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid port")
	}
	return port, nil
}

func scanLoopbackRange(first, last, workers int, excluded map[int]struct{}, timeout time.Duration) []ProbeResult {
	ports := openLoopbackPorts(first, last, workers, excluded, timeout)
	authIDs := configuredScanAuthIDs()
	results := make([]ProbeResult, len(ports))
	var wait sync.WaitGroup
	for index, port := range ports {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, matched := probeScannedPort(port, authIDs, timeout)
			if matched {
				results[index] = result
			}
		}()
	}
	wait.Wait()

	matches := results[:0]
	for _, result := range results {
		if result.Up {
			matches = append(matches, result)
		}
	}
	return matches
}

func configuredScanAuthIDs() []string {
	ids := knownBackendIDs()
	configured := make([]string, 0, len(ids))
	for _, id := range ids {
		key, _ := backendCredentials(id)
		if key != "" {
			configured = append(configured, id)
		}
	}
	return configured
}

func probeScannedPort(port int, authIDs []string, timeout time.Duration) (ProbeResult, bool) {
	baseURL := fmt.Sprintf("http://127.0.0.1:%d/v1", port)
	candidate := Candidate{
		ID:       fmt.Sprintf("local-%d", port),
		BaseURL:  baseURL,
		Loopback: true,
	}
	models, err := backendForCandidate(candidate).Models(timeout)
	if err == nil {
		return probeResult(candidate, models), true
	}
	var statusError *modelsStatusError
	if !errors.As(err, &statusError) || statusError.status != http.StatusUnauthorized {
		return ProbeResult{}, false
	}

	for _, authID := range authIDs {
		candidate.ID = fmt.Sprintf("%s-%d", authID, port)
		candidate.authID = authID
		candidate.NonStreaming = authID == "gpt4all"
		models, err = backendForCandidate(candidate).Models(timeout)
		if err == nil {
			return probeResult(candidate, models), true
		}
	}
	return ProbeResult{}, false
}

func probeCandidate(candidate Candidate, timeout time.Duration) (Candidate, []string) {
	primaryID := candidate.ID
	ids := candidate.authIDs
	if len(ids) == 0 {
		ids = []string{candidate.ID}
	}
	orderedIDs := append([]string{candidate.ID}, ids...)
	seen := make(map[string]struct{}, len(orderedIDs))
	for _, authID := range orderedIDs {
		if authID == "" {
			continue
		}
		if _, exists := seen[authID]; exists {
			continue
		}
		seen[authID] = struct{}{}
		candidate.authID = authID
		candidate.NonStreaming = authID == "gpt4all"
		models, err := backendForCandidate(candidate).Models(timeout)
		if err == nil {
			if authID != primaryID {
				candidate.ID = authID
			}
			return candidate, models
		}
		var statusError *modelsStatusError
		if !errors.As(err, &statusError) || statusError.status != http.StatusUnauthorized {
			return candidate, nil
		}
	}
	candidate.authID = ""
	candidate.NonStreaming = primaryID == "gpt4all"
	return candidate, nil
}

func probeResult(candidate Candidate, models []string) ProbeResult {
	return ProbeResult{
		Candidate:  candidate,
		Up:         true,
		ModelCount: len(models),
	}
}

func backendForCandidate(candidate Candidate) *Backend {
	authID := candidate.authID
	if authID == "" {
		authID = candidate.ID
	}
	item := newBackendWithCredentials(candidate.ID, candidate.BaseURL, authID)
	item.NonStreaming = candidate.NonStreaming
	return item
}

func openLoopbackPorts(first, last, workers int, excluded map[int]struct{}, timeout time.Duration) []int {
	if first < 1 {
		first = 1
	}
	if last > 65535 {
		last = 65535
	}
	if first > last || workers < 1 {
		return nil
	}

	dialTimeout := min(timeout, portDialTimeout)
	if dialTimeout <= 0 {
		return nil
	}
	jobs := make(chan int)
	open := make(chan int, workers)
	var wait sync.WaitGroup
	for range min(workers, last-first+1) {
		wait.Add(1)
		go func() {
			defer wait.Done()
			dialer := net.Dialer{Timeout: dialTimeout}
			for port := range jobs {
				address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
				connection, err := dialer.Dial("tcp", address)
				if err != nil {
					continue
				}
				_ = connection.Close()
				open <- port
			}
		}()
	}

	go func() {
		for port := first; port <= last; port++ {
			if _, skip := excluded[port]; !skip {
				jobs <- port
			}
		}
		close(jobs)
		wait.Wait()
		close(open)
	}()

	ports := make([]int, 0)
	for port := range open {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return ports
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
