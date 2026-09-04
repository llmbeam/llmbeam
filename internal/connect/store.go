package connect

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/llmbeam/llmbeam/internal/security"
)

// StoredSession is the minimal credential record needed for a future
// reconnect. It includes the pinned host identity alongside the token.
type StoredSession struct {
	Host        string  `json:"host"`
	Fingerprint string  `json:"fingerprint,omitempty"`
	Session     Session `json:"session"`
}

// SessionStore stores connector credentials in a per-user private file.
type SessionStore struct{ path string }

// NewSessionStore creates a store. An empty path uses the platform user config
// directory under llmbeam. Unix files are created with mode 0600; on Windows
// the file inherits the user's private config-directory ACL.
func NewSessionStore(path string) (*SessionStore, error) {
	if path == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("find user config directory: %w", err)
		}
		path = filepath.Join(configDir, "llmbeam", "connector.json")
	}
	return &SessionStore{path: path}, nil
}

// Path returns the credential file path.
func (store *SessionStore) Path() string { return store.path }

// Save atomically writes a connector credential with restrictive permissions.
func (store *SessionStore) Save(record StoredSession) error {
	if store == nil || store.path == "" || record.Host == "" || record.Session.Token == "" {
		return errors.New("invalid connector session record")
	}
	if record.Fingerprint != "" {
		normalized, err := security.NormalizeFingerprint(record.Fingerprint)
		if err != nil {
			return err
		}
		record.Fingerprint = normalized
	}
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create connector config directory: %w", err)
	}
	payload, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encode connector session: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(store.path), ".connector-*.tmp")
	if err != nil {
		return fmt.Errorf("create connector session temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set connector session permissions: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write connector session: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, store.path); err != nil {
		return fmt.Errorf("replace connector session: %w", err)
	}
	if err := os.Chmod(store.path, 0o600); err != nil {
		return fmt.Errorf("set connector session permissions: %w", err)
	}
	return nil
}

// Load reads a stored credential record.
func (store *SessionStore) Load() (StoredSession, error) {
	if store == nil || store.path == "" {
		return StoredSession{}, errors.New("invalid connector session store")
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		return StoredSession{}, err
	}
	var record StoredSession
	if err := json.Unmarshal(data, &record); err != nil {
		return StoredSession{}, fmt.Errorf("decode connector session: %w", err)
	}
	if record.Host == "" || record.Session.Token == "" {
		return StoredSession{}, errors.New("stored connector session is invalid")
	}
	return record, nil
}

// Delete removes the stored credential, if present.
func (store *SessionStore) Delete() error {
	if store == nil || store.path == "" {
		return errors.New("invalid connector session store")
	}
	if err := os.Remove(store.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
