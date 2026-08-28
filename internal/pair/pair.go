// Package pair implements one-time pairing codes and in-memory sessions.
package pair

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	codeLength   = 8
	codeAlphabet = "23456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// Session represents a device that successfully redeemed a pairing code.
type Session struct {
	Token   string
	ID      string
	Device  string
	IP      string
	Created time.Time
}

// CodeUpdate is published whenever the active pairing code changes.
type CodeUpdate struct {
	Code    string
	Expires time.Time
}

// Manager owns the current pairing code and all active in-memory sessions.
type Manager struct {
	mu       sync.Mutex
	code     string
	codeExp  time.Time
	ttl      time.Duration
	sessions map[string]Session
	updates  chan CodeUpdate
	now      func() time.Time
	random   io.Reader
}

// NewManager creates a manager and immediately generates its first code.
func NewManager(ttl time.Duration) *Manager {
	return newManager(ttl, time.Now, rand.Reader)
}

func newManager(ttl time.Duration, now func() time.Time, random io.Reader) *Manager {
	m := &Manager{
		ttl:      ttl,
		sessions: make(map[string]Session),
		updates:  make(chan CodeUpdate, 1),
		now:      now,
		random:   random,
	}
	m.rotateLocked(now())
	return m
}

// CodeUpdates returns the single-consumer stream of pairing code rotations.
// The channel always retains the latest state, including the initial code.
func (m *Manager) CodeUpdates() <-chan CodeUpdate {
	return m.updates
}

// Code returns the current pairing code, rotating it first if it has expired.
func (m *Manager) Code() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateIfExpiredLocked(m.now())
	return m.code
}

// CodeExpiry returns the expiration time of the current pairing code.
func (m *Manager) CodeExpiry() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rotateIfExpiredLocked(m.now())
	return m.codeExp
}

// Redeem exchanges a valid, unexpired code for a new session. A successful
// exchange immediately rotates the code, making it single-use.
func (m *Manager) Redeem(code, device, ip string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if !now.Before(m.codeExp) {
		m.rotateLocked(now)
		return Session{}, false
	}

	normalized := normalize(code)
	if len(normalized) != len(m.code) || subtle.ConstantTimeCompare([]byte(normalized), []byte(m.code)) != 1 {
		return Session{}, false
	}

	token := m.randomBytes(32)
	id := m.randomBytes(3)
	session := Session{
		Token:   base64.RawURLEncoding.EncodeToString(token),
		ID:      "d_" + strings.ToLower(base64.RawURLEncoding.EncodeToString(id)),
		Device:  device,
		IP:      ip,
		Created: now,
	}
	m.sessions[session.Token] = session
	m.rotateLocked(now)
	return session, true
}

// Session looks up an active session by its opaque token.
func (m *Manager) Session(token string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	return session, ok
}

// Sessions returns a stable snapshot of all active sessions.
func (m *Manager) Sessions() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Session, 0, len(m.sessions))
	for _, session := range m.sessions {
		out = append(out, session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}

func (m *Manager) rotateIfExpiredLocked(now time.Time) {
	if !now.Before(m.codeExp) {
		m.rotateLocked(now)
	}
}

func (m *Manager) rotateLocked(now time.Time) {
	previous := m.code
	for {
		candidate := m.randomCode()
		if candidate == previous {
			continue
		}
		m.code = candidate
		m.codeExp = now.Add(m.ttl)
		m.publishCodeLocked()
		return
	}
}

func (m *Manager) publishCodeLocked() {
	update := CodeUpdate{Code: m.code, Expires: m.codeExp}
	select {
	case m.updates <- update:
		return
	default:
	}
	select {
	case <-m.updates:
	default:
	}
	m.updates <- update
}

func (m *Manager) randomCode() string {
	var code strings.Builder
	code.Grow(codeLength)
	for code.Len() < codeLength {
		var b [1]byte
		if _, err := io.ReadFull(m.random, b[:]); err != nil {
			panic("crypto/rand unavailable: " + err.Error())
		}

		// Rejection sampling avoids modulo bias because the alphabet length does
		// not evenly divide the 256 possible byte values.
		limit := 256 - (256 % len(codeAlphabet))
		if int(b[0]) >= limit {
			continue
		}
		code.WriteByte(codeAlphabet[int(b[0])%len(codeAlphabet)])
	}
	return code.String()
}

func (m *Manager) randomBytes(size int) []byte {
	b := make([]byte, size)
	if _, err := io.ReadFull(m.random, b); err != nil {
		panic("crypto/rand unavailable: " + err.Error())
	}
	return b
}

func normalize(code string) string {
	return strings.ToUpper(strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return r
	}, code))
}

// RateLimiter blocks an IP after maxFails failures inside window and keeps it
// blocked for lockout. All state is process-local.
type RateLimiter struct {
	mu       sync.Mutex
	maxFails int
	window   time.Duration
	lockout  time.Duration
	states   map[string]*attemptState
	now      func() time.Time
}

type attemptState struct {
	failures    []time.Time
	lockedUntil time.Time
}

// NewRateLimiter creates a per-IP failed-attempt limiter.
func NewRateLimiter(maxFails int, window, lockout time.Duration) *RateLimiter {
	return newRateLimiter(maxFails, window, lockout, time.Now)
}

func newRateLimiter(maxFails int, window, lockout time.Duration, now func() time.Time) *RateLimiter {
	if maxFails < 1 {
		panic("pair: maxFails must be positive")
	}
	return &RateLimiter{
		maxFails: maxFails,
		window:   window,
		lockout:  lockout,
		states:   make(map[string]*attemptState),
		now:      now,
	}
}

// Allow reports whether an IP may make another pairing attempt.
func (r *RateLimiter) Allow(ip string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	state, ok := r.states[ip]
	if !ok {
		return true
	}

	now := r.now()
	if now.Before(state.lockedUntil) {
		return false
	}
	state.lockedUntil = time.Time{}
	state.failures = recentFailures(state.failures, now, r.window)
	if len(state.failures) == 0 {
		delete(r.states, ip)
	}
	return true
}

// Fail records a failed pairing attempt for an IP.
func (r *RateLimiter) Fail(ip string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	state, ok := r.states[ip]
	if !ok {
		state = &attemptState{}
		r.states[ip] = state
	}
	if now.Before(state.lockedUntil) {
		return
	}
	state.failures = append(recentFailures(state.failures, now, r.window), now)
	if len(state.failures) >= r.maxFails {
		state.lockedUntil = now.Add(r.lockout)
	}
}

func recentFailures(failures []time.Time, now time.Time, window time.Duration) []time.Time {
	recent := failures[:0]
	for _, failure := range failures {
		if now.Sub(failure) < window {
			recent = append(recent, failure)
		}
	}
	return recent
}
