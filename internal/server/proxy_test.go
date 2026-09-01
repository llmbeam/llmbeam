package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shao-hua-li/scanchat/internal/backend"
	"github.com/shao-hua-li/scanchat/internal/pair"
)

func fakeStreamingUpstream(t *testing.T) (*httptest.Server, <-chan map[string]any) {
	t.Helper()
	received := make(chan map[string]any, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		received <- payload
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		flusher := w.(http.Flusher)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	upstream := httptest.NewServer(mux)
	t.Cleanup(upstream.Close)
	return upstream, received
}

func newProxyTestServer(t *testing.T, backends []*backend.Backend) (*httptest.Server, *pair.Manager) {
	t.Helper()
	server, pairs := newTestServer(t, backends, nil)
	return server, pairs
}

func TestChatProxyStreamsSSE(t *testing.T) {
	upstream, received := fakeStreamingUpstream(t)
	server, pairs := newProxyTestServer(t, []*backend.Backend{{ID: "fake", BaseURL: upstream.URL + "/v1"}})
	cookie, _ := pairUp(t, server, pairs, "test browser")
	body := `{"model":"fake/org/m1","messages":[{"role":"user","content":"hi"}],"temperature":0.2}`
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(cookie)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	stream, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, stream)
	}
	if response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("Content-Type = %q", response.Header.Get("Content-Type"))
	}
	if !strings.Contains(string(stream), "Hel") || !strings.Contains(string(stream), "[DONE]") {
		t.Fatalf("SSE stream = %q", stream)
	}

	upstreamPayload := <-received
	if upstreamPayload["model"] != "org/m1" || upstreamPayload["stream"] != true {
		t.Fatalf("upstream payload = %+v", upstreamPayload)
	}
	if upstreamPayload["temperature"] != 0.2 {
		t.Fatalf("transparent request field was lost: %+v", upstreamPayload)
	}
}

func TestChatSendsAndRefreshesBackendAuth(t *testing.T) {
	t.Setenv("SCANCHAT_CUSTOM_1_API_KEY", "old-key")
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt := requests.Add(1)
		if attempt == 1 {
			if r.Header.Get("Authorization") != "Bearer old-key" {
				t.Errorf("first Authorization = %q", r.Header.Get("Authorization"))
			}
			if err := os.Setenv("SCANCHAT_CUSTOM_1_API_KEY", "new-key"); err != nil {
				t.Errorf("set refreshed key: %v", err)
			}
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "Bearer new-key" {
			t.Errorf("refreshed Authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)

	item := backend.NewBackend("custom-1", upstream.URL)
	server, pairs := newProxyTestServer(t, []*backend.Backend{item})
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat",
		strings.NewReader(`{"model":"custom-1/m","messages":[]}`))
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want one authenticated retry", requests.Load())
	}
}

func TestChatUnknownModelIs400(t *testing.T) {
	server, pairs := newProxyTestServer(t, nil)
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat",
		strings.NewReader(`{"model":"ghost/m","messages":[]}`))
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.StatusCode)
	}
}

func TestChatBackendDownIs502(t *testing.T) {
	server, pairs := newProxyTestServer(t,
		[]*backend.Backend{{ID: "dead", BaseURL: "http://127.0.0.1:1/v1"}})
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat",
		strings.NewReader(`{"model":"dead/m","messages":[]}`))
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
}

func TestChatRejectsBadRequest(t *testing.T) {
	server, pairs := newProxyTestServer(t, nil)
	cookie, _ := pairUp(t, server, pairs, "test")
	for _, body := range []string{`{`, `{}`, `{"model":7}`, `{"model":"ghost/m"} {}`} {
		request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat", strings.NewReader(body))
		request.AddCookie(cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %q status = %d, want 400", body, response.StatusCode)
		}
	}
}

func TestChatMapsUpstreamErrorsTo502(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
	}{
		{name: "status", status: http.StatusTooManyRequests, contentType: "application/json"},
		{name: "non SSE", status: http.StatusOK, contentType: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(`{"error":"upstream detail"}`))
			}))
			t.Cleanup(upstream.Close)
			server, pairs := newProxyTestServer(t,
				[]*backend.Backend{{ID: "fake", BaseURL: upstream.URL}})
			cookie, _ := pairUp(t, server, pairs, "test")
			request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat",
				strings.NewReader(`{"model":"fake/m","messages":[]}`))
			request.AddCookie(cookie)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("status = %d, want 502", response.StatusCode)
			}
		})
	}
}

func TestChatDoesNotFollowUpstreamRedirects(t *testing.T) {
	followed := make(chan struct{}, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed <- struct{}{}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(target.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	server, pairs := newProxyTestServer(t, []*backend.Backend{{ID: "fake", BaseURL: redirect.URL}})
	cookie, _ := pairUp(t, server, pairs, "test")
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/chat",
		strings.NewReader(`{"model":"fake/m","messages":[]}`))
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", response.StatusCode)
	}
	select {
	case <-followed:
		t.Fatal("chat proxy followed redirect outside the fixed completion path")
	default:
	}
}

func TestChatCancellationPropagatesUpstream(t *testing.T) {
	cancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(cancelled)
	}))
	t.Cleanup(upstream.Close)
	server, pairs := newProxyTestServer(t, []*backend.Backend{{ID: "fake", BaseURL: upstream.URL}})
	cookie, _ := pairUp(t, server, pairs, "test")
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/chat",
		strings.NewReader(`{"model":"fake/m","messages":[]}`))
	request.AddCookie(cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	response.Body.Close()

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("downstream cancellation did not reach upstream")
	}
}
