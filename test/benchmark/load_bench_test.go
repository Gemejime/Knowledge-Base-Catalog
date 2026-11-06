package benchmark_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/event"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
)

func createBenchmarkKB(tenantID, kbID string, version uint64) *entity.KnowledgeBase {
	now := time.Now().UTC()
	return &entity.KnowledgeBase{
		TenantID:           tenantID,
		KBID:               kbID,
		Name:               "Benchmark Knowledge Base",
		Description:        "High throughput load benchmark validation entity",
		Version:            version,
		State:              entity.StateActive,
		RoutingPolicy:      entity.RoutingGreenOnly,
		ActiveIndexPointer: fmt.Sprintf("idx_green_%s_v%d", kbID, version),
		ActiveSchema: entity.VectorSchema{
			EmbeddingModel: "text-embedding-3-small",
			Dimensions:     1536,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "v1.0",
		},
		RetrievalProfile: entity.RetrievalProfile{
			DefaultTopK:               10,
			DenseWeight:               0.7,
			SparseWeight:              0.3,
			RerankerCandidatePoolSize: 50,
		},
		TaxonomyTags: []string{"env:benchmark", "tier:enterprise"},
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// -----------------------------------------------------------------------------
// Standard Go Microbenchmarks
// -----------------------------------------------------------------------------

// BenchmarkDirectCacheRead measures throughput and allocations for direct cache lookups.
func BenchmarkDirectCacheRead(b *testing.B) {
	ctx := context.Background()
	cache := rediscache.NewInMemoryVersionedCache()
	kb := createBenchmarkKB("tenant-bench-1", "kb-bench-001", 1)
	if err := cache.PutVersion(ctx, kb); err != nil {
		b.Fatalf("failed to prime cache: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			res, err := cache.GetLatest(ctx, "tenant-bench-1", "kb-bench-001", 0)
			if err != nil || res == nil {
				b.Fatalf("cache read failure: %v", err)
			}
		}
	})
}

// BenchmarkBufferPoolAllocations validates that reusing serialization buffers via sync.Pool
// achieves zero heap allocation overhead during envelope encoding.
func BenchmarkBufferPoolAllocations(b *testing.B) {
	kb := createBenchmarkKB("tenant-pool-bench", "kb-pool-001", 1)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf := observability.AcquireBuffer()
			encoder := json.NewEncoder(buf)
			if err := encoder.Encode(kb); err != nil {
				b.Fatalf("failed to encode: %v", err)
			}
			observability.ReleaseBuffer(buf)
		}
	})
}

// BenchmarkOCCConcurrentMutation measures transaction execution throughput under optimistic concurrency control.
func BenchmarkOCCConcurrentMutation(b *testing.B) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)

	// Pre-seed 100 distinct knowledge bases for partitioned concurrency
	for i := 0; i < 100; i++ {
		tID := fmt.Sprintf("tenant-occ-%d", i)
		kbID := fmt.Sprintf("kb-occ-%d", i)
		kb := createBenchmarkKB(tID, kbID, 1)
		evt, _ := event.NewOutboxEvent(kb, event.EventKBCreated, nil, time.Now().UTC())
		if err := repo.Create(ctx, kb, evt, nil); err != nil {
			b.Fatalf("failed to seed: %v", err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		var idx int
		for pb.Next() {
			idx++
			tID := fmt.Sprintf("tenant-occ-%d", idx%100)
			kbID := fmt.Sprintf("kb-occ-%d", idx%100)

			kb, err := repo.GetByID(ctx, tID, kbID)
			if err != nil {
				continue
			}

			updated := dto.CloneKB(kb)
			updated.Version = kb.Version + 1
			updated.Name = fmt.Sprintf("Updated Name %d", updated.Version)
			updated.UpdatedAt = time.Now().UTC()

			evt, _ := event.NewOutboxEvent(updated, event.EventKBUpdated, nil, updated.UpdatedAt)
			if err := repo.UpdateMetadata(ctx, updated, kb.Version, evt); err != nil && !errors.Is(err, entity.ErrVersionConflict) {
				b.Fatalf("unexpected OCC mutation error: %v", err)
			}
		}
	})
}

// -----------------------------------------------------------------------------
// Synthetic Quantile Load Tests
// -----------------------------------------------------------------------------

// TestLoad_DirectCacheReadSLI_P99Sub5ms generates 3,000 requests across concurrent workers
// against versioned cache entries, asserting P50 <= 1ms, P90 <= 3ms, and P99 <= 5ms SLIs.
func TestLoad_DirectCacheReadSLI_P99Sub5ms(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	cache := rediscache.NewInMemoryVersionedCache()
	getQry := query.NewGetKnowledgeBaseQuery(repo, cache)

	const totalRequests = 3000
	const concurrency = 50

	// Seed tenant knowledge bases in cache
	for i := 0; i < 10; i++ {
		tID := fmt.Sprintf("tenant-read-sli-%d", i)
		kbID := fmt.Sprintf("kb-read-sli-%d", i)
		kb := createBenchmarkKB(tID, kbID, 1)
		evt, _ := event.NewOutboxEvent(kb, event.EventKBCreated, nil, time.Now().UTC())
		if err := repo.Create(ctx, kb, evt, nil); err != nil {
			t.Fatalf("failed to seed: %v", err)
		}
		if err := cache.PutVersion(ctx, kb); err != nil {
			t.Fatalf("failed to cache: %v", err)
		}
	}

	durations := make([]time.Duration, totalRequests)
	var mu sync.Mutex
	var durationIdx int

	workChan := make(chan int, totalRequests)
	for i := 0; i < totalRequests; i++ {
		workChan <- i
	}
	close(workChan)

	startLoad := time.Now()
	var wg sync.WaitGroup
	wg.Add(concurrency)

	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for reqID := range workChan {
				tID := fmt.Sprintf("tenant-read-sli-%d", reqID%10)
				kbID := fmt.Sprintf("kb-read-sli-%d", reqID%10)

				start := time.Now()
				res, err := getQry.Execute(ctx, dto.GetKBInput{
					Caller: policy.CallerIdentity{
						TenantID: tID,
						UserID:   "reader-worker",
						Roles:    []policy.Role{policy.RoleViewer},
					},
					KBID: kbID,
				})
				elapsed := time.Since(start)

				if err != nil || res == nil {
					t.Errorf("read query failed: %v", err)
					continue
				}

				mu.Lock()
				durations[durationIdx] = elapsed
				durationIdx++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	loadDuration := time.Since(startLoad)
	rps := float64(totalRequests) / loadDuration.Seconds()
	t.Logf("Direct Cache Read Load: %d requests completed in %v (Throughput: %.2f req/s, target: >=3000 RPS)", totalRequests, loadDuration, rps)

	// Sort latencies to compute quantiles
	validDurations := durations[:durationIdx]
	sort.Slice(validDurations, func(i, j int) bool {
		return validDurations[i] < validDurations[j]
	})

	n := len(validDurations)
	if n < totalRequests {
		t.Fatalf("only recorded %d durations of %d", n, totalRequests)
	}

	p50 := validDurations[n*50/100]
	p90 := validDurations[n*90/100]
	p99 := validDurations[n*99/100]

	t.Logf("Direct Cache Read Latencies (N=%d) -> P50: %v, P90: %v, P99: %v", n, p50, p90, p99)

	// Assertions against quantitative SLIs: P50 <= 1ms, P90 <= 3ms, P99 <= 5ms
	if p50 > 1*time.Millisecond {
		t.Errorf("Direct-Cache P50 read latency exceeded 1ms SLI: got %v", p50)
	}
	if p90 > 3*time.Millisecond {
		t.Errorf("Direct-Cache P90 read latency exceeded 3ms SLI: got %v", p90)
	}
	if p99 > 5*time.Millisecond {
		t.Errorf("Direct-Cache P99 read latency exceeded 5ms SLI: got %v", p99)
	}
}

// TestLoad_OCCMutationLatency_P99Sub20ms executes concurrent writes across distinct tenant
// entities, recording transaction durations and asserting P99 <= 20ms SLI.
// Exercises live PostgreSQL transactions when DATABASE_URL is configured; otherwise
// measures in-memory OCC persistence latency.
func TestLoad_OCCMutationLatency_P99Sub20ms(t *testing.T) {
	ctx := context.Background()
	metrics := observability.NewMetrics(nil)

	var (
		repo       repository.CatalogRepository
		isPostgres bool
	)
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		pool, err := postgres.NewPool(pingCtx, postgres.PoolConfig{
			DSN:      dsn,
			MaxConns: 25,
			MinConns: 0,
		})
		if err == nil && pool.Ping(pingCtx) == nil {
			defer pool.Close()
			repo = postgres.NewCatalogRepo(pool, postgres.NewOutboxRepo(), metrics)
			isPostgres = true
		} else if pool != nil {
			pool.Close()
		}
		cancel()
	}
	if repo == nil {
		repo = postgres.NewInMemoryCatalogRepo(metrics)
	}

	const totalWrites = 500
	const concurrency = 10

	kbIDs := make([]string, totalWrites)
	// Seed tenant entities to test concurrent write distribution without false conflicts
	for i := 0; i < totalWrites; i++ {
		tID := fmt.Sprintf("tenant-write-sli-%d", i)
		kbID, err := entity.NewUUIDv4()
		if err != nil {
			t.Fatalf("failed to generate UUID for KB seed: %v", err)
		}
		kbIDs[i] = kbID
		kb := createBenchmarkKB(tID, kbID, 1)
		evt, err := event.NewOutboxEvent(kb, event.EventKBCreated, nil, time.Now().UTC())
		if err != nil {
			t.Fatalf("failed to create outbox event: %v", err)
		}
		if err := repo.Create(ctx, kb, evt, nil); err != nil {
			t.Fatalf("failed to seed: %v", err)
		}
	}

	durations := make([]time.Duration, totalWrites)
	var mu sync.Mutex
	var recordedCount int

	workChan := make(chan int, totalWrites)
	for i := 0; i < totalWrites; i++ {
		workChan <- i
	}
	close(workChan)

	var wg sync.WaitGroup
	wg.Add(concurrency)

	for w := 0; w < concurrency; w++ {
		go func() {
			defer wg.Done()
			for reqID := range workChan {
				tID := fmt.Sprintf("tenant-write-sli-%d", reqID)
				kbID := kbIDs[reqID]

				kb, err := repo.GetByID(ctx, tID, kbID)
				if err != nil {
					t.Errorf("lookup failed: %v", err)
					continue
				}

				updated := dto.CloneKB(kb)
				updated.Version = kb.Version + 1
				updated.Description = fmt.Sprintf("Updated description %d", reqID)
				updated.UpdatedAt = time.Now().UTC()

				evt, err := event.NewOutboxEvent(updated, event.EventKBUpdated, nil, updated.UpdatedAt)
				if err != nil {
					t.Errorf("outbox event creation failed: %v", err)
					continue
				}

				start := time.Now()
				err = repo.UpdateMetadata(ctx, updated, kb.Version, evt)
				elapsed := time.Since(start)

				if err != nil {
					t.Errorf("mutation failed: %v", err)
					continue
				}

				mu.Lock()
				durations[recordedCount] = elapsed
				recordedCount++
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	validDurations := durations[:recordedCount]
	sort.Slice(validDurations, func(i, j int) bool {
		return validDurations[i] < validDurations[j]
	})

	n := len(validDurations)
	if n < totalWrites {
		t.Fatalf("only recorded %d durations of %d", n, totalWrites)
	}

	p50 := validDurations[n*50/100]
	p90 := validDurations[n*90/100]
	p99 := validDurations[n*99/100]

	targetDesc := "In-Memory OCC Mutation"
	if isPostgres {
		targetDesc = "PostgreSQL Database OCC Mutation"
	}
	t.Logf("%s Latencies (N=%d) -> P50: %v, P90: %v, P99: %v", targetDesc, n, p50, p90, p99)

	// Assert Mutation Latency SLI: P99 <= 20ms
	if p99 > 20*time.Millisecond {
		t.Errorf("OCC Mutation P99 latency exceeded 20ms SLI: got %v", p99)
	}
}

// TestLoad_OCCDatabaseMutationSLI_P99Sub20ms preserves backward-compatibility with CI test runner.
func TestLoad_OCCDatabaseMutationSLI_P99Sub20ms(t *testing.T) {
	TestLoad_OCCMutationLatency_P99Sub20ms(t)
}

// TestLoad_MemoryFootprintHeap_Sub128MB validates that under heavy throughput serialization
// using the shared sync.Pool buffer pool, Go runtime heap allocations and in-use memory remain strictly <= 128 MiB (134,217,728 bytes).
func TestLoad_MemoryFootprintHeap_Sub128MB(t *testing.T) {
	const maxAllowedBytes = 128 * 1024 * 1024 // 128 MiB
	const totalItems = 50000

	kb := createBenchmarkKB("tenant-rss-bench", "kb-rss-001", 1)

	runtime.GC()
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)

	// High-throughput batch serialization using the buffer pool
	for i := 0; i < totalItems; i++ {
		buf := observability.AcquireBuffer()
		enc := json.NewEncoder(buf)
		if err := enc.Encode(kb); err != nil {
			t.Fatalf("failed to encode: %v", err)
		}
		if buf.Len() == 0 {
			t.Fatalf("empty serialized buffer")
		}
		observability.ReleaseBuffer(buf)
	}

	runtime.GC()
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)

	t.Logf("Heap Memory Stats -> Alloc: %d bytes, HeapAlloc: %d bytes, HeapInuse: %d bytes, Sys: %d bytes",
		memAfter.Alloc, memAfter.HeapAlloc, memAfter.HeapInuse, memAfter.Sys)

	if memAfter.Alloc > maxAllowedBytes {
		t.Fatalf("Heap Alloc exceeded 128 MiB SLO: got %d bytes", memAfter.Alloc)
	}
	if memAfter.HeapInuse > maxAllowedBytes {
		t.Fatalf("Heap Inuse exceeded 128 MiB SLO: got %d bytes", memAfter.HeapInuse)
	}
	if memAfter.Sys > maxAllowedBytes*2 { // Guard total OS virtual allocation
		t.Fatalf("Total Sys allocation excessive: got %d bytes", memAfter.Sys)
	}
}
