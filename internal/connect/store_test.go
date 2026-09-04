package connect

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionStoreRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "connector.json")
	store, err := NewSessionStore(path)
	if err != nil {
		t.Fatal(err)
	}
	record := StoredSession{Host: "https://llmbeam.local:8443", Fingerprint: "AABB" + "CCDD" + "EEFF" + "0011" + "2233" + "4455" + "6677" + "8899" + "AABB" + "CCDD" + "EEFF" + "0011" + "2233" + "4455" + "6677" + "8899", Session: Session{Token: "secret", Expires: time.Now().Add(time.Hour)}}
	if err := store.Save(record); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.Host != record.Host || loaded.Session.Token != record.Session.Token {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if mode := func() os.FileMode { info, _ := os.Stat(path); return info.Mode().Perm() }(); mode != 0o600 {
		t.Fatalf("permissions=%o, want 600", mode)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file still exists: %v", err)
	}
}
