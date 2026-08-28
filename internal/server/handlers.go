package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const maxPairRequestBytes = 4 << 10

func jsonError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func jsonOK(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]bool{"ok": true})
}

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.Allow(ip) {
		jsonError(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}

	var request struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(w, r, &request, maxPairRequestBytes); err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request")
		return
	}

	device := deviceName(r.UserAgent())
	session, ok := s.pairs.Redeem(request.Code, device, ip)
	if !ok {
		s.limiter.Fail(ip)
		jsonError(w, http.StatusUnauthorized, "invalid_or_expired_code")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    session.Token,
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	logger.Info("paired", "device", session.Device, "ip", session.IP)
	jsonOK(w, map[string]string{"device_id": session.ID, "name": session.Device})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	session, _ := sessionFromRequest(r)
	jsonOK(w, map[string]string{"device_id": session.ID})
}

func (s *Server) handleModels(w http.ResponseWriter, _ *http.Request) {
	jsonOK(w, map[string]any{"models": s.registry.ListModels()})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any, limit int64) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func deviceName(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "iPhone"):
		return "iPhone"
	case strings.Contains(userAgent, "iPad"):
		return "iPad"
	case strings.Contains(userAgent, "Android") && strings.Contains(userAgent, "Mobile"):
		return "Android phone"
	case strings.Contains(userAgent, "Android"):
		return "Android device"
	case strings.Contains(userAgent, "Macintosh"):
		return "Mac"
	case strings.Contains(userAgent, "Windows"):
		return "Windows PC"
	case strings.TrimSpace(userAgent) == "":
		return "Unknown device"
	default:
		return "Browser"
	}
}
