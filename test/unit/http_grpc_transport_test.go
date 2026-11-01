package unit_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	catalogv1 "github.com/gemejime/rag-kb-catalog-service/api/proto/v1"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
	grpctransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc/interceptors"
	httptransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/http"
)

const testJWTSecret = "unit-test-hmac-secret-key-2026"

func makeTestJWT(t *testing.T, tenantID, sub string, roles []string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claimsMap := map[string]any{
		"tenant_id": tenantID,
		"sub":       sub,
		"roles":     roles,
	}
	claimsBytes, err := json.Marshal(claimsMap)
	if err != nil {
		t.Fatalf("failed to marshal claims: %v", err)
	}
	payload := base64.RawURLEncoding.EncodeToString(claimsBytes)
	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, []byte(testJWTSecret))
	_, _ = mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig
}

type testHarness struct {
	repo       *postgres.InMemoryCatalogRepo
	httpServer *httptest.Server
	grpcClient catalogv1.CatalogServiceClient
	logBuf     *bytes.Buffer
	cleanup    func()
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	logBuf := &bytes.Buffer{}
	logger := observability.NewJSONLogger(logBuf, slog.LevelDebug)
	metrics := observability.NewMetrics(nil)
	repo := postgres.NewInMemoryCatalogRepo(metrics)
	trigger := workflow.NewNoopIngestionTrigger()

	createCmd := command.NewCreateKnowledgeBaseCommand(repo, nil)
	updateCmd := command.NewUpdateKnowledgeBaseCommand(repo, nil)
	triggerCmd := command.NewTriggerReindexingCommand(repo, nil, trigger)
	promoteCmd := command.NewPromotePointerCommand(repo, nil)
	abortCmd := command.NewAbortReindexingCommand(repo, nil)
	deleteCmd := command.NewDeleteKnowledgeBaseCommand(repo)
	getQuery := query.NewGetKnowledgeBaseQuery(repo, nil)
	listQuery := query.NewListKnowledgeBasesQuery(repo)

	readiness := func(_ context.Context) (bool, bool, error) {
		return true, true, nil
	}

	httpHandler := httptransport.NewHandler(
		createCmd,
		updateCmd,
		triggerCmd,
		promoteCmd,
		abortCmd,
		deleteCmd,
		getQuery,
		listQuery,
		readiness,
		nil,
	)
	router := httptransport.NewRouter(httpHandler, testJWTSecret, logger, metrics)
	httpSrv := httptest.NewServer(router)

	lis := bufconn.Listen(1024 * 1024)
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.UnaryAuthLoggingInterceptor(testJWTSecret, logger, metrics)),
	)
	catalogv1.RegisterCatalogServiceServer(grpcSrv, grpctransport.NewCatalogServer(
		createCmd,
		updateCmd,
		triggerCmd,
		promoteCmd,
		abortCmd,
		deleteCmd,
		getQuery,
		listQuery,
	))
	go func() {
		_ = grpcSrv.Serve(lis)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("failed to create bufconn grpc client: %v", err)
	}

	return &testHarness{
		repo:       repo,
		httpServer: httpSrv,
		grpcClient: catalogv1.NewCatalogServiceClient(conn),
		logBuf:     logBuf,
		cleanup: func() {
			_ = conn.Close()
			grpcSrv.Stop()
			httpSrv.Close()
		},
	}
}

func TestPgBouncerExecModePoolConfiguration(t *testing.T) {
	cfg, err := postgres.ParsePoolConfig(postgres.PoolConfig{
		DSN: "postgres://catalog_user:secret@localhost:6432/rag_catalog?sslmode=disable",
	})
	if err != nil {
		t.Fatalf("ParsePoolConfig failed: %v", err)
	}
	if cfg.ConnConfig.DefaultQueryExecMode != pgx.QueryExecModeExec {
		t.Fatalf("expected DefaultQueryExecMode == pgx.QueryExecModeExec (%v), got %v", pgx.QueryExecModeExec, cfg.ConnConfig.DefaultQueryExecMode)
	}
}

func TestSecuredLoggerRedactsCredentialsAndTokens(t *testing.T) {
	var buf bytes.Buffer
	logger := observability.NewJSONLogger(&buf, slog.LevelInfo)
	logger.Info("testing redaction",
		slog.String("authorization", "Bearer eyJhbGciOiJIUzI1NiJ9.secret.sig"),
		slog.String("database_url", "postgres://admin:supersecret@db:5432/catalog"),
		slog.String("password", "raw-password-123"),
		slog.String("tenant_id", "tenant-safe"),
	)

	output := buf.String()
	if strings.Contains(output, "eyJhbGciOiJIUzI1NiJ9") || strings.Contains(output, "supersecret") || strings.Contains(output, "raw-password-123") {
		t.Fatalf("sensitive credential leaked in structured log output: %s", output)
	}
	if !strings.Contains(output, observability.RedactedValue) {
		t.Fatalf("expected %q in log output, got: %s", observability.RedactedValue, output)
	}
}

func TestHTTPProbesDocAPIAndFullLifecycle(t *testing.T) {
	h := newTestHarness(t)
	defer h.cleanup()

	// 1. Probes & Doc API endpoints (/healthz, /ready, /docs, /redoc, /openapi.json, /api/docs, /metrics)
	for _, ep := range []string{"/healthz", "/ready", "/docs", "/redoc", "/openapi.json", "/api/docs", "/metrics"} {
		resp, err := http.Get(h.httpServer.URL + ep)
		if err != nil {
			t.Fatalf("GET %s failed: %v", ep, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200 OK from %s, got %d", ep, resp.StatusCode)
		}
	}

	token := makeTestJWT(t, "tenant-enterprise", "user-admin-1", []string{"Owner"})

	// 2. Create Knowledge Base with Idempotency-Key
	createBody := `{
		"name": "hipaa-clinical-kb",
		"description": "Primary clinical corpus",
		"vector_schema": {
			"embedding_model": "text-embedding-3-large",
			"dimensions": 1536,
			"distance_metric": "COSINE",
			"schema_version": "v1"
		},
		"retrieval_profile": {
			"default_top_k": 15,
			"dense_weight": 0.7,
			"sparse_weight": 0.3,
			"reranker_candidate_pool_size": 30,
			"cross_encoder_confidence_threshold": 0.65
		},
		"taxonomy_tags": ["env:production", "compliance:hipaa"]
	}`

	req, _ := http.NewRequest(http.MethodPost, h.httpServer.URL+"/v1/knowledge-bases", strings.NewReader(createBody))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Idempotency-Key", "idemp-create-001")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/knowledge-bases failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created, got %d", resp.StatusCode)
	}

	var created dto.KBResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}
	kbID := created.KnowledgeBase.KBID
	if created.KnowledgeBase.Version != 1 || created.KnowledgeBase.State != entity.StateActive {
		t.Fatalf("unexpected created KB state: %+v", created.KnowledgeBase)
	}

	// 3. Replaying identical Idempotency-Key returns same KB without duplicate conflict
	reqReplay, _ := http.NewRequest(http.MethodPost, h.httpServer.URL+"/v1/knowledge-bases", strings.NewReader(createBody))
	reqReplay.Header.Set("Authorization", "Bearer "+token)
	reqReplay.Header.Set("Idempotency-Key", "idemp-create-001")
	respReplay, err := http.DefaultClient.Do(reqReplay)
	if err != nil {
		t.Fatalf("replay POST failed: %v", err)
	}
	defer respReplay.Body.Close()
	if respReplay.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 Created on idempotent replay, got %d", respReplay.StatusCode)
	}

	// 4. Attempting in-place vector schema mutation via PATCH returns 422 Unprocessable Entity
	illegalPatch := `{
		"expected_version": 1,
		"attempted_schema_mutation": {
			"embedding_model": "text-embedding-3-large",
			"dimensions": 3072,
			"distance_metric": "COSINE",
			"schema_version": "v1"
		}
	}`
	patchReq, _ := http.NewRequest(http.MethodPatch, h.httpServer.URL+"/v1/knowledge-bases/"+kbID, strings.NewReader(illegalPatch))
	patchReq.Header.Set("Authorization", "Bearer "+token)
	patchResp, err := http.DefaultClient.Do(patchReq)
	if err != nil {
		t.Fatalf("PATCH failed: %v", err)
	}
	_ = patchResp.Body.Close()
	if patchResp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 Unprocessable Entity on structural schema mutation, got %d", patchResp.StatusCode)
	}

	// 5. Trigger Reindexing (:triggerReindexing -> 202 Accepted)
	reindexBody := `{
		"expected_version": 1,
		"target_blue_schema": {
			"embedding_model": "text-embedding-3-large",
			"dimensions": 3072,
			"distance_metric": "DOT_PRODUCT",
			"schema_version": "v2"
		}
	}`
	trigReq, _ := http.NewRequest(http.MethodPost, h.httpServer.URL+"/v1/knowledge-bases/"+kbID+":triggerReindexing", strings.NewReader(reindexBody))
	trigReq.Header.Set("Authorization", "Bearer "+token)
	trigResp, err := http.DefaultClient.Do(trigReq)
	if err != nil {
		t.Fatalf("POST :triggerReindexing failed: %v", err)
	}
	_ = trigResp.Body.Close()
	if trigResp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted from :triggerReindexing, got %d", trigResp.StatusCode)
	}

	// 6. Simulate authoritative delta lag = 50 -> :promotePointer must return 409 Conflict
	if err := h.repo.SetMigrationDeltaLag("tenant-enterprise", kbID, 50); err != nil {
		t.Fatalf("SetMigrationDeltaLag failed: %v", err)
	}
	promReqLag, _ := http.NewRequest(http.MethodPost, h.httpServer.URL+"/v1/knowledge-bases/"+kbID+":promotePointer", strings.NewReader(`{"expected_version": 2}`))
	promReqLag.Header.Set("Authorization", "Bearer "+token)
	promRespLag, err := http.DefaultClient.Do(promReqLag)
	if err != nil {
		t.Fatalf("POST :promotePointer failed: %v", err)
	}
	_ = promRespLag.Body.Close()
	if promRespLag.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 Conflict when delta_lag_records=50, got %d", promRespLag.StatusCode)
	}

	// 7. Drain authoritative delta lag to 0 -> :promotePointer succeeds (200 OK)
	if err := h.repo.SetMigrationDeltaLag("tenant-enterprise", kbID, 0); err != nil {
		t.Fatalf("SetMigrationDeltaLag(0) failed: %v", err)
	}
	promReqOK, _ := http.NewRequest(http.MethodPost, h.httpServer.URL+"/v1/knowledge-bases/"+kbID+":promotePointer", strings.NewReader(`{"expected_version": 2, "verified_delta_lag_records": 0}`))
	promReqOK.Header.Set("Authorization", "Bearer "+token)
	promRespOK, err := http.DefaultClient.Do(promReqOK)
	if err != nil {
		t.Fatalf("POST :promotePointer (lag=0) failed: %v", err)
	}
	defer promRespOK.Body.Close()
	if promRespOK.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from :promotePointer when lag=0, got %d", promRespOK.StatusCode)
	}

	var promoted dto.KBResponse
	if err := json.NewDecoder(promRespOK.Body).Decode(&promoted); err != nil {
		t.Fatalf("failed to decode promote response: %v", err)
	}
	if promoted.KnowledgeBase.Version != 3 || promoted.KnowledgeBase.ActiveSchema.Dimensions != 3072 {
		t.Fatalf("expected version 3 and dimensions 3072 after promotion, got %+v", promoted.KnowledgeBase)
	}
}

func TestOptimisticConcurrency20GoroutinesAndGRPCTenantSpoofing(t *testing.T) {
	h := newTestHarness(t)
	defer h.cleanup()

	token := makeTestJWT(t, "tenant-occ", "user-editor-1", []string{"Editor"})
	ctx := metadata.NewOutgoingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))

	// 1. gRPC CreateKnowledgeBase
	created, err := h.grpcClient.CreateKnowledgeBase(ctx, &catalogv1.CreateKBRequest{
		TenantId:    "tenant-occ",
		Name:        "occ-stress-kb",
		Description: "Initial description",
		VectorSchema: &catalogv1.VectorSchema{
			EmbeddingModel: "bge-m3",
			Dimensions:     1024,
			DistanceMetric: catalogv1.DistanceMetric_DISTANCE_METRIC_COSINE,
			SchemaVersion:  "v1",
		},
		RetrievalProfile: &catalogv1.RetrievalProfile{
			DefaultTopK:                     10,
			DenseWeight:                     0.8,
			SparseWeight:                    0.2,
			RerankerCandidatePoolSize:       25,
			CrossEncoderConfidenceThreshold: 0.5,
		},
		TaxonomyTags: []string{"env:production"},
	})
	if err != nil {
		t.Fatalf("gRPC CreateKnowledgeBase failed: %v", err)
	}
	kbID := created.KnowledgeBase.KbId

	// 2. Verify gRPC tenant_id spoofing defense (F-09): passing mismatched TenantId in payload returns PermissionDenied
	_, spoofErr := h.grpcClient.GetKnowledgeBase(ctx, &catalogv1.GetKBRequest{
		TenantId: "other-tenant-spoof",
		KbId:     kbID,
	})
	if status.Code(spoofErr) != codes.PermissionDenied {
		t.Fatalf("expected codes.PermissionDenied on cross-tenant gRPC payload spoofing, got %v", spoofErr)
	}

	// 3. Fire 20 concurrent goroutines updating the same KB at expected_version = 1.
	// Exactly 1 must succeed (codes.OK), and 19 must fail with codes.Aborted (OCC version conflict).
	const concurrency = 20
	var (
		wg            sync.WaitGroup
		successCount  atomic.Int32
		conflictCount atomic.Int32
	)
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			desc := fmt.Sprintf("updated by worker %d", i)
			_, updateErr := h.grpcClient.UpdateKnowledgeBase(ctx, &catalogv1.UpdateKBRequest{
				TenantId:        "tenant-occ",
				KbId:            kbID,
				ExpectedVersion: 1,
				Description:     &desc,
			})
			if updateErr == nil {
				successCount.Add(1)
			} else if status.Code(updateErr) == codes.Aborted {
				conflictCount.Add(1)
			} else {
				t.Errorf("unexpected error from concurrent update: %v", updateErr)
			}
		}()
	}
	wg.Wait()

	if successCount.Load() != 1 || conflictCount.Load() != 19 {
		t.Fatalf("expected exactly 1 success and 19 OCC Aborted conflicts, got success=%d conflict=%d",
			successCount.Load(), conflictCount.Load())
	}

	// 4. Verify version incremented by 1 to version 2 and outbox recorded exactly 2 events (KBCreated + 1 KBUpdated)
	got, err := h.grpcClient.GetKnowledgeBase(ctx, &catalogv1.GetKBRequest{
		TenantId: "tenant-occ",
		KbId:     kbID,
	})
	if err != nil {
		t.Fatalf("gRPC GetKnowledgeBase failed: %v", err)
	}
	if got.KnowledgeBase.Version != 2 {
		t.Fatalf("expected version 2 after 20-way race, got %d", got.KnowledgeBase.Version)
	}
	if len(h.repo.OutboxEvents()) != 2 {
		t.Fatalf("expected exactly 2 committed outbox events, got %d", len(h.repo.OutboxEvents()))
	}
}
