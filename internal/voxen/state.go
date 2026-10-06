package voxen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Health records the latest outcome per provider so routing can prefer channels
// that worked recently and avoid ones that just failed. Credentials are never
// stored; a key fingerprint retires old records after rotation.
type Health struct {
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastFailure time.Time `json:"last_failure,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
}

// failureCooldown is how long a channel failure demotes a provider.
const failureCooldown = 10 * time.Minute

type healthStore struct {
	path string
	mu   sync.Mutex
}

var storeLock sync.Mutex

func newHealthStore(home string) *healthStore {
	return &healthStore{path: filepath.Join(home, "health.json")}
}

func (s *healthStore) load() map[string]Health {
	m := map[string]Health{}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func (s *healthStore) all() map[string]Health {
	storeLock.Lock()
	defer storeLock.Unlock()
	return s.load()
}

func (s *healthStore) record(provider, fingerprint string, err error) {
	storeLock.Lock()
	defer storeLock.Unlock()
	m := s.load()
	h := m[provider]
	if h.Fingerprint != fingerprint {
		h = Health{Fingerprint: fingerprint}
	}
	now := time.Now().UTC()
	if err == nil {
		h.LastSuccess = now
	} else {
		h.LastFailure, h.LastError = now, err.Error()
	}
	m[provider] = h
	if os.MkdirAll(filepath.Dir(s.path), 0o700) != nil {
		return
	}
	b, _ := json.MarshalIndent(m, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// degraded reports whether the most recent outcome is a channel failure that is
// still inside the cooldown window.
func (h Health) degraded(fingerprint string, now time.Time) bool {
	if h.Fingerprint != fingerprint || h.LastFailure.IsZero() {
		return false
	}
	return h.LastFailure.After(h.LastSuccess) && now.Sub(h.LastFailure) < failureCooldown
}
