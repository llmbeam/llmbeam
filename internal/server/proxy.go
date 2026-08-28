package server

import "net/http"

// handleChat is implemented by the streaming proxy task. Keeping the route in
// place now lets the authentication and origin middleware be tested first.
func (s *Server) handleChat(w http.ResponseWriter, _ *http.Request) {
	jsonError(w, http.StatusBadRequest, "unknown_model")
}
