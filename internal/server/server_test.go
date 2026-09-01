package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/shao-hua-li/llmbeam/internal/backend"
	"github.com/shao-hua-li/llmbeam/internal/pair"
)

func newTestServer(t *testing.T, backends []*backend.Backend, static fstest.MapFS) (*httptest.Server, *pair.Manager) {
	t.Helper()
	pairs := pair.NewManager(time.Minute)
	registry := backend.NewRegistry(backends, 500*time.Millisecond)
	limiter := pair.NewRateLimiter(5, time.Minute, 5*time.Minute)
	server := httptest.NewServer(New(pairs, registry, limiter, static).Handler())
	t.Cleanup(server.Close)
	return server, pairs
}

func pairUp(t *testing.T, server *httptest.Server, pairs *pair.Manager, userAgent string) (*http.Cookie, map[string]string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/pair",
		strings.NewReader(`{"code":"`+pairs.Code()+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("pair status = %d, body = %s", response.StatusCode, body)
	}
	var payload map[string]string
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range response.Cookies() {
		if cookie.Name == sessionCookieName {
			return cookie, payload
		}
	}
	t.Fatal("pair response did not set session cookie")
	return nil, nil
}

func TestPairFlowSetsHardenedSessionCookie(t *testing.T) {
	server, pairs := newTestServer(t, nil, nil)
	cookie, payload := pairUp(t, server, pairs, "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0)")
	if !cookie.HttpOnly || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes = %+v", cookie)
	}
	if cookie.Secure {
		t.Fatal("plain-LAN HTTP cookie cannot use Secure")
	}
	if cookie.MaxAge != 60*60*24*30 {
		t.Fatalf("cookie MaxAge = %d", cookie.MaxAge)
	}
	if payload["device_id"] == "" || payload["name"] != "iPhone" {
		t.Fatalf("pair response = %+v", payload)
	}
}

func TestUnauthenticatedModelsRejected(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	response, err := http.Get(server.URL + "/api/models")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", response.StatusCode)
	}
}

func TestAuthenticatedSessionAndModels(t *testing.T) {
	server, pairs := newTestServer(t, nil, nil)
	cookie, paired := pairUp(t, server, pairs, "test browser")
	for _, path := range []string{"/api/session", "/api/models"} {
		request, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		request.AddCookie(cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("GET %s status = %d", path, response.StatusCode)
		}
		if path == "/api/session" {
			var payload map[string]string
			_ = json.NewDecoder(response.Body).Decode(&payload)
			if payload["device_id"] != paired["device_id"] {
				response.Body.Close()
				t.Fatalf("session response = %+v", payload)
			}
		}
		response.Body.Close()
	}
}

func TestBadPairCodeIsRateLimited(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	for attempt := 1; attempt <= 6; attempt++ {
		response, err := http.Post(server.URL+"/api/pair", "application/json",
			strings.NewReader(`{"code":"WRONGCOD"}`))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		want := http.StatusUnauthorized
		if attempt == 6 {
			want = http.StatusTooManyRequests
		}
		if response.StatusCode != want {
			t.Fatalf("attempt %d status = %d, want %d", attempt, response.StatusCode, want)
		}
	}
}

func TestMalformedPairRequestRejected(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	for _, body := range []string{`{`, `{"code":"ABC", "extra":true}`, `{"code":"ABC"} {}`} {
		response, err := http.Post(server.URL+"/api/pair", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, response.StatusCode)
		}
	}
}

func TestCrossOriginPostBlocked(t *testing.T) {
	server, pairs := newTestServer(t, nil, nil)
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat", strings.NewReader(`{}`))
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://evil.example")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", response.StatusCode)
	}
}

func TestSameOriginPostAllowed(t *testing.T) {
	server, pairs := newTestServer(t, nil, nil)
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat", strings.NewReader(`{}`))
	request.AddCookie(cookie)
	request.Header.Set("Origin", server.URL)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want chat stub's 400", response.StatusCode)
	}
}

func TestHealthzIsOpenAndHasSecurityHeaders(t *testing.T) {
	server, _ := newTestServer(t, nil, nil)
	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if response.Header.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.Contains(response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
		t.Fatalf("security headers missing: %v", response.Header)
	}
}

func TestStaticAssetsAreOpen(t *testing.T) {
	static := fstest.MapFS{"index.html": {Data: []byte("<h1>llmbeam</h1>")}}
	server, _ := newTestServer(t, nil, static)
	response, err := http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "llmbeam") {
		t.Fatalf("static response status=%d body=%q", response.StatusCode, body)
	}
}
