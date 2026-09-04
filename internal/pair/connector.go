package pair

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

const (
	connectorCodeLength = 6
	connectorSessionTTL = 30 * 24 * time.Hour
)

// ConnectorSession is a credential for an OpenAI-compatible connector.
// Connector sessions are intentionally separate from browser sessions.
type ConnectorSession struct {
	Token     string
	ID        string
	ClientID  string
	PublicKey string
	Device    string
	IP        string
	Created   time.Time
	Expires   time.Time
}

// ConnectorManager owns short-lived connector codes and longer-lived
// connector sessions. All state is process-local and disappears on exit.
type ConnectorManager struct {
	mu          sync.Mutex
	code        string
	codeExp     time.Time
	codeTTL     time.Duration
	sessionTTL  time.Duration
	sessions    map[string]ConnectorSession
	now         func() time.Time
	random      io.Reader
	ipLimiter   *RateLimiter
	codeLimiter *RateLimiter
}

// NewConnectorManager creates a connector manager with a five-minute code and
// thirty-day connector sessions.
func NewConnectorManager() *ConnectorManager {
	return newConnectorManager(5*time.Minute, connectorSessionTTL, time.Now, rand.Reader)
}

func newConnectorManager(codeTTL, sessionTTL time.Duration, now func() time.Time, random io.Reader) *ConnectorManager {
	if codeTTL <= 0 || sessionTTL <= 0 {
		panic("pair: connector TTLs must be positive")
	}
	manager := &ConnectorManager{
		codeTTL:     codeTTL,
		sessionTTL:  sessionTTL,
		sessions:    make(map[string]ConnectorSession),
		now:         now,
		random:      random,
		ipLimiter:   newRateLimiter(5, time.Minute, 5*time.Minute, now),
		codeLimiter: newRateLimiter(5, time.Minute, 5*time.Minute, now),
	}
	manager.rotateLocked(now())
	return manager
}

// Code returns the current connector code, rotating it after expiry.
func (m *ConnectorManager) Code() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateIfExpiredLocked(m.now())
	return m.code
}

// CodeExpiry returns the expiry of the current connector code.
func (m *ConnectorManager) CodeExpiry() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateIfExpiredLocked(m.now())
	return m.codeExp
}

// Redeem exchanges a valid connector code for a new connector session.
func (m *ConnectorManager) Redeem(code, clientID, publicKey, device, ip string) (ConnectorSession, bool) {
	if !m.ipLimiter.Allow(ip) || !m.codeLimiter.Allow(normalize(code)) {
		return ConnectorSession{}, false
	}
	normalized := normalize(code)
	if len(normalized) != connectorCodeLength || strings.TrimSpace(clientID) == "" || len(publicKey) > 4096 {
		m.recordFailure(normalized, ip)
		return ConnectorSession{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	if !now.Before(m.codeExp) || subtle.ConstantTimeCompare([]byte(normalized), []byte(m.code)) != 1 {
		m.recordFailureLocked(normalized, ip)
		return ConnectorSession{}, false
	}

	token := m.randomBytes(32)
	id := m.randomBytes(3)
	session := ConnectorSession{
		Token:     base64.RawURLEncoding.EncodeToString(token),
		ID:        "c_" + strings.ToLower(base64.RawURLEncoding.EncodeToString(id)),
		ClientID:  strings.TrimSpace(clientID),
		PublicKey: publicKey,
		Device:    device,
		IP:        ip,
		Created:   now,
		Expires:   now.Add(m.sessionTTL),
	}
	m.sessions[session.Token] = session
	m.rotateLocked(now)
	return session, true
}

// Session validates and returns an unexpired connector session.
func (m *ConnectorManager) Session(token string) (ConnectorSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	if !ok {
		return ConnectorSession{}, false
	}
	if !m.now().Before(session.Expires) {
		delete(m.sessions, token)
		return ConnectorSession{}, false
	}
	return session, true
}

// Refresh rotates a valid connector session token.
func (m *ConnectorManager) Refresh(token, ip string) (ConnectorSession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	if !ok || !m.now().Before(session.Expires) || (ip != "" && session.IP != ip) {
		if ok && !m.now().Before(session.Expires) {
			delete(m.sessions, token)
		}
		return ConnectorSession{}, false
	}
	now := m.now()
	newToken := base64.RawURLEncoding.EncodeToString(m.randomBytes(32))
	session.Token = newToken
	session.Created = now
	session.Expires = now.Add(m.sessionTTL)
	delete(m.sessions, token)
	m.sessions[newToken] = session
	return session, true
}

// Revoke invalidates a connector session.
func (m *ConnectorManager) Revoke(token string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[token]; !ok {
		return false
	}
	delete(m.sessions, token)
	return true
}

// Sessions returns a stable snapshot for device-management UIs.
func (m *ConnectorManager) Sessions() []ConnectorSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	out := make([]ConnectorSession, 0, len(m.sessions))
	for token, session := range m.sessions {
		if !now.Before(session.Expires) {
			delete(m.sessions, token)
			continue
		}
		out = append(out, session)
	}
	return out
}

func (m *ConnectorManager) recordFailure(code, ip string) {
	m.ipLimiter.Fail(ip)
	m.codeLimiter.Fail(code)
}

func (m *ConnectorManager) recordFailureLocked(code, ip string) {
	m.ipLimiter.Fail(ip)
	m.codeLimiter.Fail(code)
}

func (m *ConnectorManager) rotateIfExpiredLocked(now time.Time) {
	if !now.Before(m.codeExp) {
		m.rotateLocked(now)
	}
}

func (m *ConnectorManager) rotateLocked(now time.Time) {
	previous := m.code
	for {
		candidate := randomCode(m.random, connectorCodeLength)
		if candidate == previous {
			continue
		}
		m.code = candidate
		m.codeExp = now.Add(m.codeTTL)
		return
	}
}

func (m *ConnectorManager) randomBytes(size int) []byte {
	value := make([]byte, size)
	if _, err := io.ReadFull(m.random, value); err != nil {
		panic(fmt.Sprintf("pair: crypto/rand unavailable: %v", err))
	}
	return value
}

func randomCode(random io.Reader, length int) string {
	var code strings.Builder
	code.Grow(length)
	for code.Len() < length {
		var value [1]byte
		if _, err := io.ReadFull(random, value[:]); err != nil {
			panic(fmt.Sprintf("pair: crypto/rand unavailable: %v", err))
		}
		limit := 256 - (256 % len(codeAlphabet))
		if int(value[0]) >= limit {
			continue
		}
		code.WriteByte(codeAlphabet[int(value[0])%len(codeAlphabet)])
	}
	return code.String()
}
