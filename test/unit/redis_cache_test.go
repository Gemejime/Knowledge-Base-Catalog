package unit

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
	httptransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/http"
)

// redisCRC16LookupTable is the XMODEM CRC16 lookup table used by Redis Cluster.
var redisCRC16LookupTable = [256]uint16{
	0x0000, 0x1021, 0x2042, 0x3063, 0x4084, 0x50a5, 0x60c6, 0x70e7,
	0x8108, 0x9129, 0xa14a, 0xb16b, 0xc18c, 0xd1ad, 0xe1ce, 0xf1ef,
	0x1231, 0x0210, 0x3273, 0x2252, 0x52b5, 0x4294, 0x72f7, 0x62d6,
	0x9339, 0x8318, 0xb37b, 0xa35a, 0xd3bd, 0xc39c, 0xf3ff, 0xe3de,
	0x2462, 0x3443, 0x0420, 0x1401, 0x64e6, 0x74c7, 0x44a4, 0x5485,
	0xa56a, 0xb54b, 0x8528, 0x9509, 0xe5ee, 0xf5cf, 0xc5ac, 0xd58d,
	0x3653, 0x2672, 0x1611, 0x0630, 0x76d7, 0x66f6, 0x5695, 0x46b4,
	0xb75b, 0xa77a, 0x9719, 0x8738, 0xf7df, 0xe7fe, 0xd79d, 0xc7bc,
	0x48c4, 0x58e5, 0x6886, 0x78a7, 0x0840, 0x1861, 0x2802, 0x3823,
	0xc9cc, 0xd9ed, 0xe98e, 0xf9af, 0x8948, 0x9969, 0xa90a, 0xb92b,
	0x5af5, 0x4ad4, 0x7ab7, 0x6a96, 0x1a71, 0x0a50, 0x3a33, 0x2a12,
	0xdbfd, 0xcbdc, 0xfbbf, 0xeb9e, 0x9b79, 0x8b58, 0xbb3b, 0xab1a,
	0x6ca6, 0x7c87, 0x4ce4, 0x5cc5, 0x2c22, 0x3c03, 0x0c60, 0x1c41,
	0xedae, 0xfd8f, 0xcdec, 0xddcd, 0xad2a, 0xbd0b, 0x8d68, 0x9d49,
	0x7e97, 0x6eb6, 0x5ed5, 0x4ef4, 0x3e13, 0x2e32, 0x1e51, 0x0e70,
	0xff9f, 0xefbe, 0xdfdd, 0xcffc, 0xbf1b, 0xaf3a, 0x9f59, 0x8f78,
	0x9188, 0x81a9, 0xb1ca, 0xa1eb, 0xd10c, 0xc12d, 0xf14e, 0xe16f,
	0x1080, 0x00a1, 0x30c2, 0x20e3, 0x5004, 0x4025, 0x7046, 0x6067,
	0x83b9, 0x9398, 0xa3fb, 0xb3da, 0xc33d, 0xd31c, 0xe37f, 0xf35e,
	0x02b1, 0x1290, 0x22f3, 0x32d2, 0x4235, 0x5214, 0x6277, 0x7256,
	0xb5ea, 0xa5cb, 0x95a8, 0x8589, 0xf56e, 0xe54f, 0xd52c, 0xc50d,
	0x34e2, 0x24c3, 0x14a0, 0x0481, 0x7466, 0x6447, 0x5424, 0x4405,
	0xa7db, 0xb7fa, 0x8799, 0x97b8, 0xe75f, 0xf77e, 0xc71d, 0xd73c,
	0x26d3, 0x36f2, 0x0691, 0x16b0, 0x6657, 0x7676, 0x4615, 0x5634,
	0xd94c, 0xc96d, 0xf90e, 0xe92f, 0x99c8, 0x89e9, 0xb98a, 0xa9ab,
	0x5844, 0x4865, 0x7806, 0x6827, 0x18c0, 0x08e1, 0x3882, 0x28a3,
	0xcb7d, 0xdb5c, 0xeb3f, 0xfb1e, 0x8bf9, 0x9bd8, 0xabbb, 0xbb9a,
	0x4a75, 0x5a54, 0x6a37, 0x7a16, 0x0af1, 0x1ad0, 0x2ab3, 0x3a92,
	0xfd2e, 0xed0f, 0xdd6c, 0xcd4d, 0xbdaa, 0xad8b, 0x9de8, 0x8dc9,
	0x7c26, 0x6c07, 0x5c64, 0x4c45, 0x3ca2, 0x2c83, 0x1ce0, 0x0cc1,
	0xef1f, 0xff3e, 0xcf5d, 0xdf7c, 0xaf9b, 0xbfba, 0x8fd9, 0x9ff8,
	0x6e17, 0x7e36, 0x4e55, 0x5e74, 0x2e93, 0x3eb2, 0x0ed1, 0x1ef0,
}

// calculateRedisClusterCRC16 computes the CRC16 checksum according to Redis Cluster specifications.
func calculateRedisClusterCRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc = (crc << 8) ^ redisCRC16LookupTable[byte(crc>>8)^b]
	}
	return crc
}

// computeRedisClusterSlot determines the 0-16383 hash slot for a key, honoring the first {...} hash tag.
func computeRedisClusterSlot(key string) uint16 {
	s := key
	if start := strings.IndexByte(s, '{'); start != -1 {
		if end := strings.IndexByte(s[start+1:], '}'); end != -1 {
			s = s[start+1 : start+1+end]
		}
	}
	return calculateRedisClusterCRC16([]byte(s)) & 16383
}

func TestRedisVersionedKeyGenerationAndCRC16CoLocation(t *testing.T) {
	tenants := []string{"tenant-100", "enterprise-us-east", "acme-corp", "t1"}
	kbIDs := []string{"0192e2fb-83bb-7000-8000-000000000001", "kb-9999", "kb-uuid-sample"}

	for _, tenant := range tenants {
		for _, kbID := range kbIDs {
			vKey1 := rediscache.VersionedKey(tenant, kbID, 1)
			vKey2 := rediscache.VersionedKey(tenant, kbID, 42)
			lKey := rediscache.LatestKey(tenant, kbID)

			// 1. Verify key structure format
			expectedV1 := "kb:{" + tenant + "}:" + kbID + ":v1"
			expectedL := "kb:{" + tenant + "}:" + kbID + ":latest_version"
			if vKey1 != expectedV1 {
				t.Fatalf("expected versioned key %q, got %q", expectedV1, vKey1)
			}
			if lKey != expectedL {
				t.Fatalf("expected latest key %q, got %q", expectedL, lKey)
			}

			// 2. Verify CRC16 slot invariance (ADR-003): all keys for the tenant MUST map to identical hash slot
			slotV1 := computeRedisClusterSlot(vKey1)
			slotV2 := computeRedisClusterSlot(vKey2)
			slotL := computeRedisClusterSlot(lKey)

			if slotV1 != slotL {
				t.Fatalf("CRC16 CROSSSLOT violation: versioned key %s (slot %d) != latest key %s (slot %d)", vKey1, slotV1, lKey, slotL)
			}
			if slotV2 != slotL {
				t.Fatalf("CRC16 CROSSSLOT violation: versioned key %s (slot %d) != latest key %s (slot %d)", vKey2, slotV2, lKey, slotL)
			}
		}
	}
}

func TestMonotonicPointerAdvancementAndTombstoneCaching(t *testing.T) {
	ctx := context.Background()
	cache := rediscache.NewInMemoryVersionedCache()

	tenantID := "tenant-alpha"
	kbID := "kb-uuid-001"

	kbV1 := &entity.KnowledgeBase{
		TenantID:           tenantID,
		KBID:               kbID,
		Name:               "Finance KB",
		Version:            1,
		State:              entity.StateActive,
		RoutingPolicy:      entity.RoutingGreenOnly,
		ActiveIndexPointer: "index-green-v1",
		ActiveSchema: entity.VectorSchema{
			EmbeddingModel: "bge-m3",
			Dimensions:     1024,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "1.0.0",
		},
		RetrievalProfile: entity.RetrievalProfile{DefaultTopK: 10, DenseWeight: 0.7, SparseWeight: 0.3},
		TaxonomyTags:     []string{"domain:finance"},
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}

	// 1. Put v1
	if err := cache.PutVersion(ctx, kbV1); err != nil {
		t.Fatalf("failed to put version 1: %v", err)
	}

	gotV1, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || gotV1 == nil || gotV1.Version != 1 {
		t.Fatalf("expected version 1 from cache, got %+v (err: %v)", gotV1, err)
	}

	// 2. Put v2
	kbV2 := *kbV1
	kbV2.Version = 2
	kbV2.Name = "Finance KB Updated"
	if err := cache.PutVersion(ctx, &kbV2); err != nil {
		t.Fatalf("failed to put version 2: %v", err)
	}

	gotV2, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || gotV2 == nil || gotV2.Version != 2 {
		t.Fatalf("expected version 2 from cache, got %+v (err: %v)", gotV2, err)
	}

	// 3. Monotonic invariant: Put v1 again (simulating delayed out-of-order write) -> pointer MUST NOT regress
	if err := cache.PutVersion(ctx, kbV1); err != nil {
		t.Fatalf("failed to put version 1 again: %v", err)
	}

	gotStillV2, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || gotStillV2 == nil || gotStillV2.Version != 2 {
		t.Fatalf("monotonic pointer regressed: expected version 2, got %+v", gotStillV2)
	}

	// 4. min_version causal freshness check: minVersion 3 should yield cache miss (nil, nil)
	gotStale, err := cache.GetLatest(ctx, tenantID, kbID, 3)
	if err != nil {
		t.Fatalf("unexpected error on minVersion check: %v", err)
	}
	if gotStale != nil {
		t.Fatalf("expected cache miss for minVersion 3 when latest is 2, got %+v", gotStale)
	}

	// 5. Tombstone caching check: soft-deleted state at incremented version
	kbV3Tombstone := *kbV1
	kbV3Tombstone.Version = 3
	kbV3Tombstone.State = entity.StateTombstoned
	if err := cache.PutVersion(ctx, &kbV3Tombstone); err != nil {
		t.Fatalf("failed to put tombstone version: %v", err)
	}

	gotTombstone, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || gotTombstone == nil {
		t.Fatalf("expected tombstoned record from cache, got nil (err: %v)", err)
	}
	if gotTombstone.State != entity.StateTombstoned || gotTombstone.Version != 3 {
		t.Fatalf("expected tombstone state at version 3, got state %s version %d", gotTombstone.State, gotTombstone.Version)
	}
}

func TestGetKnowledgeBaseQueryWithCacheFallbackAndTombstone(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()

	tenantID := "tenant-beta"
	kbID := "kb-uuid-999"
	caller := policy.CallerIdentity{TenantID: tenantID, UserID: "user-test", Roles: []policy.Role{policy.RoleOwner}}

	now := time.Now().UTC()
	initialKB := &entity.KnowledgeBase{
		TenantID:           tenantID,
		KBID:               kbID,
		Name:               "Engineering Docs",
		Version:            1,
		State:              entity.StateActive,
		RoutingPolicy:      entity.RoutingGreenOnly,
		ActiveIndexPointer: "index-green-v1",
		ActiveSchema: entity.VectorSchema{
			EmbeddingModel: "bge-m3",
			Dimensions:     1024,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "1.0.0",
		},
		RetrievalProfile: entity.RetrievalProfile{DefaultTopK: 5, DenseWeight: 0.8, SparseWeight: 0.2},
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if err := repo.Create(ctx, initialKB, nil, nil); err != nil {
		t.Fatalf("failed to insert initial KB into repo: %v", err)
	}

	getQuery := query.NewGetKnowledgeBaseQuery(repo, cache)

	// 1. First read: cache miss -> queries repo -> backfills cache
	resp1, err := getQuery.Execute(ctx, dto.GetKBInput{
		Caller: caller,
		KBID:   kbID,
	})
	if err != nil || resp1.KnowledgeBase.Version != 1 {
		t.Fatalf("expected KB version 1 on first read, got %+v (err: %v)", resp1, err)
	}

	// Verify it was backfilled in cache
	cached, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || cached == nil || cached.Version != 1 {
		t.Fatalf("expected cache to be backfilled with version 1, got %+v (err: %v)", cached, err)
	}

	// 2. min_version > cached_version: triggers cache-aside fallback
	// Manually bump repo version to 2 without updating cache
	initialKB.Version = 2
	initialKB.Name = "Engineering Docs V2"
	if err := repo.UpdateMetadata(ctx, initialKB, 1, nil); err != nil {
		t.Fatalf("failed to update repo to v2: %v", err)
	}

	// Caller asks for min_version = 2
	resp2, err := getQuery.Execute(ctx, dto.GetKBInput{
		Caller:     caller,
		KBID:       kbID,
		MinVersion: 2,
	})
	if err != nil || resp2.KnowledgeBase.Version != 2 {
		t.Fatalf("expected KB version 2 on min_version=2 read, got %+v (err: %v)", resp2, err)
	}

	// Verify cache was updated to version 2
	cachedV2, err := cache.GetLatest(ctx, tenantID, kbID, 0)
	if err != nil || cachedV2 == nil || cachedV2.Version != 2 {
		t.Fatalf("expected cache to be updated to version 2, got %+v (err: %v)", cachedV2, err)
	}

	// 3. Soft delete: tombstone is cached at version 3
	deleteCmd := command.NewDeleteKnowledgeBaseCommand(repo, cache)
	if err := deleteCmd.Execute(ctx, dto.DeleteKBInput{
		Caller:          caller,
		KBID:            kbID,
		ExpectedVersion: 2,
	}); err != nil {
		t.Fatalf("delete command failed: %v", err)
	}

	// Subsequent GetKnowledgeBase reads cache, discovers tombstone, and returns ErrKnowledgeBaseNotFound
	_, err = getQuery.Execute(ctx, dto.GetKBInput{
		Caller: caller,
		KBID:   kbID,
	})
	if !errors.Is(err, entity.ErrKnowledgeBaseNotFound) {
		t.Fatalf("expected ErrKnowledgeBaseNotFound on tombstoned entity, got %v", err)
	}
}

func TestReadinessProbeWithRedisAndPostgresHealth(t *testing.T) {
	memCache := rediscache.NewInMemoryVersionedCache()
	pgHealthy := true

	readinessCheck := func(ctx context.Context) (bool, bool, error) {
		if !pgHealthy {
			return false, true, errors.New("postgres connection pool exhausted")
		}
		if err := memCache.Ping(ctx); err != nil {
			return true, false, err
		}
		return true, true, nil
	}

	handler := httptransport.NewHandler(
		nil, nil, nil, nil, nil, nil, nil, nil,
		readinessCheck,
		nil,
	)

	// 1. Both healthy -> 200 OK
	w1 := httptest.NewRecorder()
	r1 := httptest.NewRequest(http.MethodGet, "/ready", nil)
	handler.Ready(w1, r1)

	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200 OK when healthy, got %d (body: %s)", w1.Code, w1.Body.String())
	}
	var readyResp map[string]string
	if err := json.Unmarshal(w1.Body.Bytes(), &readyResp); err != nil {
		t.Fatalf("failed to parse ready JSON: %v", err)
	}
	if readyResp["status"] != "ready" || readyResp["postgres"] != "connected" || readyResp["redis_cluster"] != "connected" {
		t.Fatalf("unexpected ready response payload: %+v", readyResp)
	}

	// 2. Redis failing -> 503 Service Unavailable
	memCache.SetFailing(true)
	w2 := httptest.NewRecorder()
	r2 := httptest.NewRequest(http.MethodGet, "/ready", nil)
	handler.Ready(w2, r2)

	if w2.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when redis is down, got %d", w2.Code)
	}

	// 3. Redis recovers, PostgreSQL failing -> 503 Service Unavailable
	memCache.SetFailing(false)
	pgHealthy = false
	w3 := httptest.NewRecorder()
	r3 := httptest.NewRequest(http.MethodGet, "/ready", nil)
	handler.Ready(w3, r3)

	if w3.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 Service Unavailable when postgres is down, got %d", w3.Code)
	}
}
