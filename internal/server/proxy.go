package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"time"
)

const (
	maxChatRequestBytes = 10 << 20
	maxUpstreamError    = 4 << 10
	responseHeaderWait  = 30 * time.Second
)

func newUpstreamClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = responseHeaderWait
	transport.DisableCompression = true
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// handleChat proxies a chat request to the selected backend and streams the
// OpenAI-style SSE response back without buffering it in memory.
func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	var payload map[string]json.RawMessage
	if err := decodeJSON(w, r, &payload, maxChatRequestBytes); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request")
		return
	}

	var modelID string
	if err := json.Unmarshal(payload["model"], &modelID); err != nil || modelID == "" {
		jsonError(w, http.StatusBadRequest, "bad_request")
		return
	}
	selected, upstreamModel, ok := s.registry.Resolve(modelID)
	if !ok {
		jsonError(w, http.StatusBadRequest, "unknown_model")
		return
	}

	payload["model"], _ = json.Marshal(upstreamModel)
	payload["stream"] = json.RawMessage("true")
	body, err := json.Marshal(payload)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request")
		return
	}

	upstreamRequest, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodPost,
		selected.BaseURL+"/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "internal")
		return
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("Accept", "text/event-stream")
	upstreamRequest.Header.Set("Accept-Encoding", "identity")

	response, err := s.upstream.Do(upstreamRequest)
	if err != nil {
		jsonError(w, http.StatusBadGateway, "backend_unreachable")
		return
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		responseBody, _ := io.ReadAll(io.LimitReader(response.Body, maxUpstreamError))
		logger.Warn("upstream error",
			"backend", selected.ID,
			"status", response.StatusCode,
			"body", string(responseBody),
		)
		jsonError(w, http.StatusBadGateway, "backend_error")
		return
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		logger.Warn("upstream returned non-SSE response",
			"backend", selected.ID,
			"content_type", response.Header.Get("Content-Type"),
		)
		jsonError(w, http.StatusBadGateway, "backend_error")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		jsonError(w, http.StatusInternalServerError, "streaming_unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	_, copyErr := io.CopyBuffer(flushWriter{writer: w, flusher: flusher}, response.Body, make([]byte, 32<<10))
	if copyErr != nil && r.Context().Err() == nil {
		logger.Warn("upstream stream interrupted", "backend", selected.ID, "error", copyErr)
	}
}

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (w flushWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	if written > 0 {
		w.flusher.Flush()
	}
	return written, err
}
