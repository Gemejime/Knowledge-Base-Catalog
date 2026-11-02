package temporal_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"go.temporal.io/sdk/client"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/temporal"
)

type mockWorkflowRun struct {
	client.WorkflowRun
	runID string
}

func (m *mockWorkflowRun) GetID() string {
	return "test-wf-id"
}

func (m *mockWorkflowRun) GetRunID() string {
	return m.runID
}

type mockTemporalSDKClient struct {
	client.Client
	executeFunc func(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error)
	closed      bool
}

func (m *mockTemporalSDKClient) ExecuteWorkflow(ctx context.Context, options client.StartWorkflowOptions, workflow interface{}, args ...interface{}) (client.WorkflowRun, error) {
	if m.executeFunc != nil {
		return m.executeFunc(ctx, options, workflow, args...)
	}
	return &mockWorkflowRun{runID: "default-run-id"}, nil
}

func (m *mockTemporalSDKClient) Close() {
	m.closed = true
}

func TestTemporalClientTriggerBackfillDAG_Success(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	var capturedOptions client.StartWorkflowOptions
	var capturedWorkflow string
	var capturedReq workflow.BackfillTriggerRequest

	mockSDK := &mockTemporalSDKClient{
		executeFunc: func(ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{}) (client.WorkflowRun, error) {
			capturedOptions = options
			if name, ok := wf.(string); ok {
				capturedWorkflow = name
			}
			if len(args) > 0 {
				if r, ok := args[0].(workflow.BackfillTriggerRequest); ok {
					capturedReq = r
				}
			}
			return &mockWorkflowRun{runID: "test-run-12345"}, nil
		},
	}

	cfg := temporal.Config{
		Namespace:    "rag-ns",
		TaskQueue:    "custom-queue",
		WorkflowName: "CustomBackfillWorkflow",
	}

	tc := temporal.NewTemporalClientWithSDK(mockSDK, cfg, logger)
	defer tc.Close()

	req := workflow.BackfillTriggerRequest{
		TenantID:           "tenant-enterprise",
		KBID:               "kb-001",
		MigrationID:        "mig-999",
		WorkflowID:         "custom-wf-id",
		SourceGreenPointer: "idx_green_v1",
		TargetBluePointer:  "idx_blue_v2",
		TargetSchema: entity.VectorSchema{
			EmbeddingModel: "text-embedding-3-large",
			Dimensions:     3072,
			DistanceMetric: entity.MetricCosine,
			SchemaVersion:  "v2.0",
		},
	}

	runID, err := tc.TriggerBackfillDAG(context.Background(), req)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if runID != "test-run-12345" {
		t.Errorf("expected runID 'test-run-12345', got: %s", runID)
	}

	if capturedOptions.ID != "custom-wf-id" {
		t.Errorf("expected workflow ID 'custom-wf-id', got: %s", capturedOptions.ID)
	}
	if capturedOptions.TaskQueue != "custom-queue" {
		t.Errorf("expected task queue 'custom-queue', got: %s", capturedOptions.TaskQueue)
	}
	if capturedWorkflow != "CustomBackfillWorkflow" {
		t.Errorf("expected workflow 'CustomBackfillWorkflow', got: %s", capturedWorkflow)
	}
	if capturedReq.KBID != "kb-001" {
		t.Errorf("expected KBID 'kb-001', got: %s", capturedReq.KBID)
	}

	tc.Close()
	if !mockSDK.closed {
		t.Errorf("expected mock client to be closed")
	}
}

func TestTemporalClientTriggerBackfillDAG_ErrorHandling(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, nil))

	mockSDK := &mockTemporalSDKClient{
		executeFunc: func(ctx context.Context, options client.StartWorkflowOptions, wf interface{}, args ...interface{}) (client.WorkflowRun, error) {
			return nil, errors.New("temporal connection refused")
		},
	}

	tc := temporal.NewTemporalClientWithSDK(mockSDK, temporal.Config{}, logger)
	defer tc.Close()

	req := workflow.BackfillTriggerRequest{
		TenantID:    "tenant-enterprise",
		KBID:        "kb-001",
		MigrationID: "mig-999",
	}

	runID, err := tc.TriggerBackfillDAG(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error from failed workflow start, got nil")
	}
	if runID != "" {
		t.Errorf("expected empty runID on error, got: %s", runID)
	}

	// Verify validation on empty fields
	invalidReq := workflow.BackfillTriggerRequest{
		TenantID: "",
		KBID:     "kb-001",
	}
	_, err = tc.TriggerBackfillDAG(context.Background(), invalidReq)
	if err == nil {
		t.Fatalf("expected error for empty tenant_id, got nil")
	}
}

func TestTemporalClientNewClientValidation(t *testing.T) {
	_, err := temporal.NewClient(temporal.Config{HostPort: ""}, nil)
	if err == nil {
		t.Fatalf("expected error for empty HostPort, got nil")
	}
}
