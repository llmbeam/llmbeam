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
	ID       string
	BaseURL  string
	Loopback bool
	authID   string
}

var wellKnown = defaultCandidates(runtime.GOOS)

var scanLoopback = func(excluded map[int]struct{}, timeout time.Duration) []ProbeResult {
	return scanLoopbackRange(firstScanPort, lastScanPort, portScanWorkers, excluded, timeout)
}

func defaultCandidates(goos string) []Candidate {
	candidates := []Candidate{
		{ID: "ollama", BaseURL: "http://127.0.0.1:11434/v1", Loopback: true},
		{ID: "lm-studio", BaseURL: "http://127.0.0.1:1234/v1", Loopback: true},
		{ID: "llama.cpp", BaseURL: "http://127.0.0.1:8080/v1", Loopback: true},
	}
	if goos == "darwin" {
		candidates = append(candidates, Candidate{
			ID:       "omlx",
			BaseURL:  "http://127.0.0.1:8000/v1",
			Loopback: true,
		})
	}
	return candidates
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
		item := backendForCandidate(candidate)
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
	ids := []string{"ollama", "lm-studio", "llama.cpp", "omlx"}
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
		models, err = backendForCandidate(candidate).Models(timeout)
		if err == nil {
			return probeResult(candidate, models), true
		}
	}
	return ProbeResult{}, false
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
	return newBackendWithCredentials(candidate.ID, candidate.BaseURL, authID)
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
