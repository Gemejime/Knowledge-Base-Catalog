package temporal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
)

// Default configurations for Temporal backfill orchestration.
const (
	DefaultNamespace    = "default"
	DefaultTaskQueue    = "rag-ingestion-backfill"
	DefaultWorkflowName = "IngestionBackfillWorkflow"
)

// Config encapsulates configuration for the Temporal IngestionTrigger adapter.
type Config struct {
	HostPort           string
	Namespace          string
	TaskQueue          string
	WorkflowName       string
	InitialInterval    time.Duration
	BackoffCoefficient float64
	MaximumInterval    time.Duration
	MaximumAttempts    int32
}

// TemporalClient wraps the Temporal Go SDK client to implement workflow.IngestionTrigger.
type TemporalClient struct {
	client       client.Client
	taskQueue    string
	workflowName string
	retryPolicy  *temporal.RetryPolicy
	logger       *slog.Logger
}

// NewClient establishes a connection to a Temporal server using the provided Config.
func NewClient(cfg Config, logger *slog.Logger) (*TemporalClient, error) {
	if logger == nil {
		logger = slog.Default()
	}
	hostPort := strings.TrimSpace(cfg.HostPort)
	if hostPort == "" {
		return nil, errors.New("temporal HostPort cannot be empty")
	}
	ns := strings.TrimSpace(cfg.Namespace)
	if ns == "" {
		ns = DefaultNamespace
	}
	tq := strings.TrimSpace(cfg.TaskQueue)
	if tq == "" {
		tq = DefaultTaskQueue
	}
	wfName := strings.TrimSpace(cfg.WorkflowName)
	if wfName == "" {
		wfName = DefaultWorkflowName
	}

	logger.Info("connecting to temporal cluster",
		slog.String("host_port", hostPort),
		slog.String("namespace", ns),
		slog.String("task_queue", tq),
	)

	c, err := client.Dial(client.Options{
		HostPort:  hostPort,
		Namespace: ns,
		Logger:    newSlogTemporalAdapter(logger),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to dial temporal cluster at %s: %w", hostPort, err)
	}

	return &TemporalClient{
		client:       c,
		taskQueue:    tq,
		workflowName: wfName,
		retryPolicy:  buildRetryPolicy(cfg),
		logger:       logger,
	}, nil
}

// NewTemporalClientWithSDK initializes a TemporalClient with an injected client.Client (for testing/mocking).
func NewTemporalClientWithSDK(c client.Client, cfg Config, logger *slog.Logger) *TemporalClient {
	if logger == nil {
		logger = slog.Default()
	}
	tq := strings.TrimSpace(cfg.TaskQueue)
	if tq == "" {
		tq = DefaultTaskQueue
	}
	wfName := strings.TrimSpace(cfg.WorkflowName)
	if wfName == "" {
		wfName = DefaultWorkflowName
	}
	return &TemporalClient{
		client:       c,
		taskQueue:    tq,
		workflowName: wfName,
		retryPolicy:  buildRetryPolicy(cfg),
		logger:       logger,
	}
}

func buildRetryPolicy(cfg Config) *temporal.RetryPolicy {
	initInterval := cfg.InitialInterval
	if initInterval < 100*time.Millisecond || initInterval > 1*time.Minute {
		initInterval = time.Second
	}
	backoff := cfg.BackoffCoefficient
	if backoff < 1.0 || backoff > 5.0 {
		backoff = 2.0
	}
	maxInterval := cfg.MaximumInterval
	if maxInterval < initInterval || maxInterval > 10*time.Minute {
		maxInterval = 10 * time.Second
	}
	maxAttempts := cfg.MaximumAttempts
	if maxAttempts < 1 || maxAttempts > 10 {
		maxAttempts = 3
	}

	return &temporal.RetryPolicy{
		InitialInterval:    initInterval,
		BackoffCoefficient: backoff,
		MaximumInterval:    maxInterval,
		MaximumAttempts:    maxAttempts,
	}
}

// Close gracefully closes the underlying Temporal SDK client.
func (t *TemporalClient) Close() {
	if t.client != nil {
		t.client.Close()
	}
}

// TriggerBackfillDAG schedules an asynchronous backfill DAG in rag-ingestion-coordinator.
func (t *TemporalClient) TriggerBackfillDAG(ctx context.Context, req workflow.BackfillTriggerRequest) (string, error) {
	if t.client == nil {
		return "", workflow.ErrIngestionSchedulerNotConfigured
	}
	tenantID := strings.TrimSpace(req.TenantID)
	kbID := strings.TrimSpace(req.KBID)
	migrationID := strings.TrimSpace(req.MigrationID)
	if tenantID == "" || kbID == "" || migrationID == "" {
		return "", errors.New("tenant_id, kb_id, and migration_id are required to trigger backfill DAG")
	}

	workflowID := strings.TrimSpace(req.WorkflowID)
	if workflowID == "" {
		workflowID = fmt.Sprintf("reindex-%s-%s", kbID, migrationID)
	}

	options := client.StartWorkflowOptions{
		ID:                       workflowID,
		TaskQueue:                t.taskQueue,
		WorkflowExecutionTimeout: 24 * time.Hour,
		WorkflowTaskTimeout:      1 * time.Minute,
		RetryPolicy:              t.retryPolicy,
	}

	start := time.Now()
	run, err := t.client.ExecuteWorkflow(ctx, options, t.workflowName, req)
	duration := time.Since(start)

	if err != nil {
		t.logger.Error("failed to dispatch ingestion backfill workflow via temporal",
			slog.String("tenant_id", tenantID),
			slog.String("kb_id", kbID),
			slog.String("migration_id", migrationID),
			slog.String("workflow_id", workflowID),
			slog.String("task_queue", t.taskQueue),
			slog.Duration("duration_ms", duration),
			slog.String("error", err.Error()),
		)
		return "", fmt.Errorf("temporal backfill workflow execution failed: %w", err)
	}

	runID := run.GetRunID()
	t.logger.Info("successfully scheduled ingestion backfill workflow",
		slog.String("tenant_id", tenantID),
		slog.String("kb_id", kbID),
		slog.String("migration_id", migrationID),
		slog.String("workflow_id", workflowID),
		slog.String("run_id", runID),
		slog.String("task_queue", t.taskQueue),
		slog.Duration("duration_ms", duration),
	)

	return runID, nil
}

// slogTemporalAdapter adapts *slog.Logger to Temporal SDK's log.Logger interface.
type slogTemporalAdapter struct {
	logger *slog.Logger
}

func newSlogTemporalAdapter(l *slog.Logger) *slogTemporalAdapter {
	return &slogTemporalAdapter{logger: l}
}

func (a *slogTemporalAdapter) Debug(msg string, keyvals ...any) {
	a.logger.Debug(msg, keyvals...)
}

func (a *slogTemporalAdapter) Info(msg string, keyvals ...any) {
	a.logger.Info(msg, keyvals...)
}

func (a *slogTemporalAdapter) Warn(msg string, keyvals ...any) {
	a.logger.Warn(msg, keyvals...)
}

func (a *slogTemporalAdapter) Error(msg string, keyvals ...any) {
	a.logger.Error(msg, keyvals...)
}
