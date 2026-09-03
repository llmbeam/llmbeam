package server

import (
	"net/http"
	"strings"
	"time"
)

func (s *Server) handleConnectorInfo(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]any{
		"object":             "connector_info",
		"code_length":        6,
		"expires_at":         s.connectors.CodeExpiry(),
		"server_fingerprint": s.serverFingerprint,
	})
}

func (s *Server) handleConnectorPair(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code            string `json:"code"`
		ClientID        string `json:"client_id"`
		ClientPublicKey string `json:"client_public_key"`
	}
	if err := decodeJSON(w, r, &request, maxPairRequestBytes); err != nil {
		openAIError(w, http.StatusBadRequest, "bad_request")
		return
	}
	clientID := strings.TrimSpace(request.ClientID)
	if clientID == "" || len(clientID) > 128 || !isASCII(clientID) ||
		strings.TrimSpace(request.ClientPublicKey) == "" || len(request.ClientPublicKey) > 4096 {
		openAIError(w, http.StatusBadRequest, "bad_request")
		return
	}
	device := deviceName(r.UserAgent())
	session, ok := s.connectors.Redeem(request.Code, clientID, request.ClientPublicKey, device, clientIP(r))
	if !ok {
		openAIError(w, http.StatusUnauthorized, "invalid_or_expired_code")
		return
	}
	jsonOK(w, map[string]any{
		"token":              session.Token,
		"device_id":          session.ID,
		"expires_at":         session.Expires,
		"server_fingerprint": s.serverFingerprint,
	})
}

func (s *Server) handleConnectorSessions(w http.ResponseWriter, _ *http.Request) {
	type sessionInfo struct {
		ID       string    `json:"device_id"`
		ClientID string    `json:"client_id"`
		Device   string    `json:"device"`
		IP       string    `json:"ip"`
		Created  time.Time `json:"created_at"`
		Expires  time.Time `json:"expires_at"`
	}
	sessions := s.connectors.Sessions()
	items := make([]sessionInfo, 0, len(sessions))
	for _, session := range sessions {
		items = append(items, sessionInfo{ID: session.ID, ClientID: session.ClientID, Device: session.Device, IP: session.IP, Created: session.Created, Expires: session.Expires})
	}
	jsonOK(w, map[string]any{"object": "list", "data": items})
}

func (s *Server) handleConnectorRefresh(w http.ResponseWriter, r *http.Request) {
	token := connectorToken(r)
	session, ok := s.connectors.Refresh(token, clientIP(r))
	if !ok {
		openAIError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	jsonOK(w, map[string]any{
		"token":              session.Token,
		"device_id":          session.ID,
		"expires_at":         session.Expires,
		"server_fingerprint": s.serverFingerprint,
	})
}

func (s *Server) handleConnectorRevoke(w http.ResponseWriter, r *http.Request) {
	if !s.connectors.Revoke(connectorToken(r)) {
		openAIError(w, http.StatusUnauthorized, "unauthenticated")
		return
	}
	jsonOK(w, map[string]bool{"revoked": true})
}

func connectorToken(r *http.Request) string {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

func isASCII(value string) bool {
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}
