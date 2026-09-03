package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

func openAIError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": code,
			"type":    "llmbeam_error",
			"code":    code,
		},
	})
}

func (s *Server) connectorAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
			openAIError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		token := strings.TrimSpace(header[len(prefix):])
		if token == "" {
			openAIError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		if _, ok := s.pairs.Session(token); !ok {
			openAIError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleOpenAIModels(w http.ResponseWriter, _ *http.Request) {
	models := s.registry.ListModels()
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, map[string]any{
			"id":       model.ID,
			"object":   "model",
			"created":  0,
			"owned_by": "llmbeam",
		})
	}
	jsonOK(w, map[string]any{"object": "list", "data": data})
}
