package pair

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestRedeemValidCodeOnce(t *testing.T) {
	m := NewManager(10 * time.Minute)
	code := m.Code()
	if len(code) != codeLength {
		t.Fatalf("code length = %d, want %d", len(code), codeLength)
	}
	for _, char := range code {
		if !strings.ContainsRune(codeAlphabet, char) {
			t.Fatalf("code contains unsupported character %q", char)
		}
	}

	session, ok := m.Redeem(code, "iPhone", "192.168.1.87")
	if !ok || session.Token == "" || !strings.HasPrefix(session.ID, "d_") {
		t.Fatalf("first redeem returned invalid session: %+v, ok=%v", session, ok)
	}
	if _, ok := m.Redeem(code, "other", "192.168.1.88"); ok {
		t.Fatal("second redeem of the same code must fail")
	}
	if next := m.Code(); next == code {
		t.Fatal("successful redemption must rotate to a different code")
	}
}

func TestRedeemNormalizesInput(t *testing.T) {
	m := NewManager(time.Minute)
	code := strings.ToLower(m.Code())
	messy := " \t" + code[:4] + "-" + code[4:] + "\n"
	if _, ok := m.Redeem(messy, "device", "ip"); !ok {
		t.Fatal("redeem should accept lowercase, whitespace, and a dash")
	}
}

func TestExpiredCodeFailsAndRotates(t *testing.T) {
	clock := newTestClock()
	random := &incrementingReader{}
	m := newManager(time.Minute, clock.Now, random)
	code := m.Code()
	oldExpiry := m.CodeExpiry()

	clock.Advance(time.Minute)
	if _, ok := m.Redeem(code, "device", "ip"); ok {
		t.Fatal("expired code must fail")
	}
	if !m.CodeExpiry().After(oldExpiry) {
		t.Fatal("expired code should be rotated for the next pairing attempt")
	}
}

func TestSessionLookupAndSnapshot(t *testing.T) {
	m := NewManager(time.Minute)
	session, ok := m.Redeem(m.Code(), "iPhone", "192.168.1.87")
	if !ok {
		t.Fatal("redeem failed")
	}

	got, ok := m.Session(session.Token)
	if !ok || got.Device != "iPhone" || got.IP != "192.168.1.87" {
		t.Fatalf("session lookup = %+v, %v", got, ok)
	}
	if _, ok := m.Session("bogus"); ok {
		t.Fatal("bogus token must not resolve")
	}

	snapshot := m.Sessions()
	if len(snapshot) != 1 || snapshot[0].Token != session.Token {
		t.Fatalf("Sessions() = %+v", snapshot)
	}
	snapshot[0].Device = "changed"
	got, _ = m.Session(session.Token)
	if got.Device != "iPhone" {
		t.Fatal("mutating a Sessions snapshot must not alter manager state")
	}
}

func TestRateLimiterLocksOnlyFailingIP(t *testing.T) {
	clock := newTestClock()
	limiter := newRateLimiter(5, time.Minute, 5*time.Minute, clock.Now)
	ip := "192.168.1.87"

	for attempt := 1; attempt <= 5; attempt++ {
		if !limiter.Allow(ip) {
			t.Fatalf("attempt %d should be allowed", attempt)
		}
		limiter.Fail(ip)
	}
	if limiter.Allow(ip) {
		t.Fatal("IP should be locked after five failures")
	}
	if !limiter.Allow("192.168.1.88") {
		t.Fatal("failures from one IP must not affect another")
	}

	clock.Advance(5 * time.Minute)
	if !limiter.Allow(ip) {
		t.Fatal("IP should be allowed after lockout expires")
	}
}

func TestRateLimiterForgetsFailuresOutsideWindow(t *testing.T) {
	clock := newTestClock()
	limiter := newRateLimiter(2, time.Minute, 5*time.Minute, clock.Now)
	ip := "10.0.0.2"

	limiter.Fail(ip)
	clock.Advance(time.Minute)
	if !limiter.Allow(ip) {
		t.Fatal("old failure should expire at the end of the window")
	}
	limiter.Fail(ip)
	if !limiter.Allow(ip) {
		t.Fatal("one recent failure should not trigger lockout")
	}
}

func TestManagerIsSafeForConcurrentUse(t *testing.T) {
	m := NewManager(time.Minute)
	code := m.Code()
	results := make(chan bool, 16)
	for i := 0; i < cap(results); i++ {
		go func() {
			_, ok := m.Redeem(code, "device", net.IPv4(192, 168, 1, 2).String())
			results <- ok
		}()
	}

	successes := 0
	for i := 0; i < cap(results); i++ {
		if <-results {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent redemption successes = %d, want 1", successes)
	}
}

type testClock struct {
	now time.Time
}

func newTestClock() *testClock {
	return &testClock{now: time.Date(2026, time.August, 28, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) Now() time.Time          { return c.now }
func (c *testClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

type incrementingReader struct {
	next byte
}

func (r *incrementingReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.next
		r.next++
	}
	return len(p), nil
}
