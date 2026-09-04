package pair

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConnectorRedeemValidCodeOnce(t *testing.T) {
	manager := NewConnectorManager()
	code := manager.Code()
	if len(code) != connectorCodeLength {
		t.Fatalf("code length = %d, want %d", len(code), connectorCodeLength)
	}
	session, ok := manager.Redeem(code, "client-1", "public-key", "Mac", "192.168.1.10")
	if !ok || session.Token == "" || !strings.HasPrefix(session.ID, "c_") {
		t.Fatalf("redeem = %+v, ok=%v", session, ok)
	}
	if session.ClientID != "client-1" || session.PublicKey != "public-key" {
		t.Fatalf("session metadata = %+v", session)
	}
	if _, ok := manager.Redeem(code, "client-2", "other-key", "PC", "192.168.1.11"); ok {
		t.Fatal("connector code must be single-use")
	}
	if _, ok := manager.Session(session.Token); !ok {
		t.Fatal("new connector session is not valid")
	}
}

func TestConnectorRedeemNormalizesCode(t *testing.T) {
	manager := NewConnectorManager()
	code := manager.Code()
	if _, ok := manager.Redeem(" "+strings.ToLower(code[:3])+"-"+strings.ToLower(code[3:])+
		" ", "client", "key", "device", "ip"); !ok {
		t.Fatal("connector code should accept case and separators")
	}
}

func TestConnectorCodeExpiryAndSessionExpiry(t *testing.T) {
	clock := newTestClock()
	manager := newConnectorManager(time.Minute, 2*time.Minute, clock.Now, &incrementingReader{})
	code := manager.Code()
	clock.Advance(time.Minute)
	if _, ok := manager.Redeem(code, "client", "key", "device", "ip"); ok {
		t.Fatal("expired connector code must fail")
	}
	code = manager.Code()
	session, ok := manager.Redeem(code, "client", "key", "device", "ip")
	if !ok {
		t.Fatal("fresh connector code failed")
	}
	clock.Advance(2 * time.Minute)
	if _, ok := manager.Session(session.Token); ok {
		t.Fatal("expired connector session must fail")
	}
}

func TestConnectorRefreshRotatesTokenAndRevoke(t *testing.T) {
	manager := NewConnectorManager()
	session, ok := manager.Redeem(manager.Code(), "client", "key", "device", "192.168.1.2")
	if !ok {
		t.Fatal("redeem failed")
	}
	refreshed, ok := manager.Refresh(session.Token, "192.168.1.2")
	if !ok || refreshed.Token == session.Token {
		t.Fatalf("refresh = %+v, ok=%v", refreshed, ok)
	}
	if _, ok := manager.Session(session.Token); ok {
		t.Fatal("old token remains valid after refresh")
	}
	if !manager.Revoke(refreshed.Token) {
		t.Fatal("revoke failed")
	}
	if _, ok := manager.Session(refreshed.Token); ok {
		t.Fatal("revoked token remains valid")
	}
}

func TestConnectorRefreshRequiresOriginalIP(t *testing.T) {
	manager := NewConnectorManager()
	session, ok := manager.Redeem(manager.Code(), "client", "key", "device", "192.168.1.2")
	if !ok {
		t.Fatal("redeem failed")
	}
	if _, ok := manager.Refresh(session.Token, "192.168.1.3"); ok {
		t.Fatal("refresh from another IP must fail")
	}
}

func TestConnectorRedeemConcurrentSingleUse(t *testing.T) {
	manager := NewConnectorManager()
	code := manager.Code()
	results := make(chan bool, 16)
	var wait sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, ok := manager.Redeem(code, "client", "key", "device", "192.168.1.2")
			results <- ok
		}()
	}
	wait.Wait()
	close(results)
	successes := 0
	for ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent connector redemptions = %d, want 1", successes)
	}
}
