package redis

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
)

// InMemoryVersionedCache provides a thread-safe, in-memory implementation of repository.VersionedCache
// that mirrors the Redis 7.2 Cluster versioned cache and monotonic Lua pointer semantics (ADR-003).
type InMemoryVersionedCache struct {
	mu       sync.RWMutex
	versions map[string][]byte
	pointers map[string]uint64
	failing  bool
}

// NewInMemoryVersionedCache constructs an initialized InMemoryVersionedCache.
func NewInMemoryVersionedCache() *InMemoryVersionedCache {
	return &InMemoryVersionedCache{
		versions: make(map[string][]byte),
		pointers: make(map[string]uint64),
	}
}

// PutVersion stores an immutable versioned Knowledge Base payload and advances the latest version pointer.
func (m *InMemoryVersionedCache) PutVersion(_ context.Context, kb *entity.KnowledgeBase) error {
	if kb == nil {
		return errors.New("cannot cache nil knowledge base")
	}
	tenantID := strings.TrimSpace(kb.TenantID)
	kbID := strings.TrimSpace(kb.KBID)
	if tenantID == "" || kbID == "" {
		return errors.New("tenant_id and kb_id are required for caching")
	}

	payload, err := json.Marshal(kb)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.failing {
		return errors.New("redis cluster unavailable")
	}

	vKey := VersionedKey(tenantID, kbID, kb.Version)
	lKey := LatestKey(tenantID, kbID)

	m.versions[vKey] = payload

	cur, exists := m.pointers[lKey]
	if !exists || kb.Version > cur {
		m.pointers[lKey] = kb.Version
	}

	return nil
}

// GetLatest retrieves the latest versioned Knowledge Base snapshot.
func (m *InMemoryVersionedCache) GetLatest(_ context.Context, tenantID, kbID string, minVersion uint64) (*entity.KnowledgeBase, error) {
	tID := strings.TrimSpace(tenantID)
	kID := strings.TrimSpace(kbID)
	if tID == "" || kID == "" {
		return nil, errors.New("tenant_id and kb_id are required for cache lookup")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.failing {
		return nil, errors.New("redis cluster unavailable")
	}

	lKey := LatestKey(tID, kID)
	latestVersion, exists := m.pointers[lKey]
	if !exists {
		return nil, nil // Cache miss
	}

	if minVersion > 0 && latestVersion < minVersion {
		return nil, nil // Stale cache miss
	}

	vKey := VersionedKey(tID, kID, latestVersion)
	payload, ok := m.versions[vKey]
	if !ok {
		return nil, nil
	}

	var kb entity.KnowledgeBase
	if err := json.Unmarshal(payload, &kb); err != nil {
		return nil, err
	}

	return dto.CloneKB(&kb), nil
}

// Ping checks in-memory cache health for readiness probes (/ready).
func (m *InMemoryVersionedCache) Ping(_ context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.failing {
		return errors.New("redis cluster ping failed")
	}
	return nil
}

// SetFailing simulates connection failure or recovery for testing readiness and chaos scenarios.
func (m *InMemoryVersionedCache) SetFailing(failing bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failing = failing
}

// Clear flushes all cached version keys and pointer entries.
func (m *InMemoryVersionedCache) Clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.versions = make(map[string][]byte)
	m.pointers = make(map[string]uint64)
}

// Close closes the in-memory cache.
func (m *InMemoryVersionedCache) Close() error {
	return nil
}
