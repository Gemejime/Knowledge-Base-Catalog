package grpctransport

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	catalogv1 "github.com/gemejime/rag-kb-catalog-service/api/proto/v1"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc/interceptors"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/http/middleware"
)

// CatalogServer implements catalogv1.CatalogServiceServer for internal service mesh communication.
type CatalogServer struct {
	catalogv1.UnimplementedCatalogServiceServer
	createCmd  *command.CreateKnowledgeBaseCommand
	updateCmd  *command.UpdateKnowledgeBaseCommand
	triggerCmd *command.TriggerReindexingCommand
	promoteCmd *command.PromotePointerCommand
	abortCmd   *command.AbortReindexingCommand
	deleteCmd  *command.DeleteKnowledgeBaseCommand
	getQuery   *query.GetKnowledgeBaseQuery
	listQuery  *query.ListKnowledgeBasesQuery
}

// NewCatalogServer constructs a CatalogServer.
func NewCatalogServer(
	createCmd *command.CreateKnowledgeBaseCommand,
	updateCmd *command.UpdateKnowledgeBaseCommand,
	triggerCmd *command.TriggerReindexingCommand,
	promoteCmd *command.PromotePointerCommand,
	abortCmd *command.AbortReindexingCommand,
	deleteCmd *command.DeleteKnowledgeBaseCommand,
	getQuery *query.GetKnowledgeBaseQuery,
	listQuery *query.ListKnowledgeBasesQuery,
) *CatalogServer {
	return &CatalogServer{
		createCmd:  createCmd,
		updateCmd:  updateCmd,
		triggerCmd: triggerCmd,
		promoteCmd: promoteCmd,
		abortCmd:   abortCmd,
		deleteCmd:  deleteCmd,
		getQuery:   getQuery,
		listQuery:  listQuery,
	}
}

// CreateKnowledgeBase implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) CreateKnowledgeBase(ctx context.Context, req *catalogv1.CreateKBRequest) (*catalogv1.KBResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	resp, err := s.createCmd.Execute(ctx, dto.CreateKBInput{
		Caller:           caller,
		Name:             req.Name,
		Description:      req.Description,
		VectorSchema:     fromProtoVectorSchema(req.VectorSchema),
		RetrievalProfile: fromProtoRetrievalProfile(req.RetrievalProfile),
		TaxonomyTags:     req.TaxonomyTags,
		IdempotencyKey:   req.IdempotencyKey,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &catalogv1.KBResponse{KnowledgeBase: toProtoKB(resp.KnowledgeBase)}, nil
}

// UpdateKnowledgeBase implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) UpdateKnowledgeBase(ctx context.Context, req *catalogv1.UpdateKBRequest) (*catalogv1.KBResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	var profile *entity.RetrievalProfile
	if req.RetrievalProfile != nil {
		p := fromProtoRetrievalProfile(req.RetrievalProfile)
		profile = &p
	}

	var attemptedMutation *entity.VectorSchema
	if req.AttemptedSchemaMutation != nil {
		vs := fromProtoVectorSchema(req.AttemptedSchemaMutation)
		attemptedMutation = &vs
	}

	var maskPaths []string
	if req.UpdateMask != nil {
		maskPaths = req.UpdateMask.Paths
	}

	resp, err := s.updateCmd.Execute(ctx, dto.UpdateKBInput{
		Caller:                  caller,
		KBID:                    req.KbId,
		ExpectedVersion:         req.ExpectedVersion,
		Name:                    req.Name,
		Description:             req.Description,
		RetrievalProfile:        profile,
		TaxonomyTags:            req.TaxonomyTags,
		TaxonomyTagsPresent:     req.TaxonomyTags != nil,
		UpdateMask:              maskPaths,
		AttemptedSchemaMutation: attemptedMutation,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &catalogv1.KBResponse{KnowledgeBase: toProtoKB(resp.KnowledgeBase)}, nil
}

// GetKnowledgeBase implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) GetKnowledgeBase(ctx context.Context, req *catalogv1.GetKBRequest) (*catalogv1.KBResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	var minVer uint64
	if req.MinVersion != nil {
		minVer = *req.MinVersion
	}

	resp, err := s.getQuery.Execute(ctx, dto.GetKBInput{
		Caller:     caller,
		KBID:       req.KbId,
		MinVersion: minVer,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &catalogv1.KBResponse{KnowledgeBase: toProtoKB(resp.KnowledgeBase)}, nil
}

// ListKnowledgeBases implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) ListKnowledgeBases(ctx context.Context, req *catalogv1.ListKBsRequest) (*catalogv1.ListKBsResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	resp, err := s.listQuery.Execute(ctx, dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: req.TaxonomyFilters,
		PageSize:        int(req.PageSize),
		PageToken:       req.PageToken,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}

	out := make([]*catalogv1.KnowledgeBase, 0, len(resp.KnowledgeBases))
	for _, kb := range resp.KnowledgeBases {
		out = append(out, toProtoKB(kb))
	}
	return &catalogv1.ListKBsResponse{
		KnowledgeBases: out,
		NextPageToken:  resp.NextPageToken,
	}, nil
}

// DeleteKnowledgeBase implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) DeleteKnowledgeBase(ctx context.Context, req *catalogv1.DeleteKBRequest) (*emptypb.Empty, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	err := s.deleteCmd.Execute(ctx, dto.DeleteKBInput{
		Caller:          caller,
		KBID:            req.KbId,
		ExpectedVersion: req.ExpectedVersion,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &emptypb.Empty{}, nil
}

// TriggerReindexing implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) TriggerReindexing(ctx context.Context, req *catalogv1.TriggerReindexingRequest) (*catalogv1.MigrationResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	resp, err := s.triggerCmd.Execute(ctx, dto.TriggerReindexingInput{
		Caller:           caller,
		KBID:             req.KbId,
		ExpectedVersion:  req.ExpectedVersion,
		TargetBlueSchema: fromProtoVectorSchema(req.TargetBlueSchema),
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}

	return &catalogv1.MigrationResponse{
		TenantId:              resp.TenantID,
		KbId:                  resp.KBID,
		MigrationId:           resp.MigrationID,
		State:                 toProtoLifecycleState(resp.State),
		RoutingPolicy:         toProtoRoutingPolicy(resp.RoutingPolicy),
		CandidateIndexPointer: resp.CandidateIndexPointer,
		Version:               resp.Version,
	}, nil
}

// PromoteIndexPointer implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) PromoteIndexPointer(ctx context.Context, req *catalogv1.PromotePointerRequest) (*catalogv1.KBResponse, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	resp, err := s.promoteCmd.Execute(ctx, dto.PromotePointerInput{
		Caller:                  caller,
		KBID:                    req.KbId,
		ExpectedVersion:         req.ExpectedVersion,
		VerifiedDeltaLagRecords: req.VerifiedDeltaLagRecords,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &catalogv1.KBResponse{KnowledgeBase: toProtoKB(resp.KnowledgeBase)}, nil
}

// AbortReindexing implements catalogv1.CatalogServiceServer.
func (s *CatalogServer) AbortReindexing(ctx context.Context, req *catalogv1.AbortReindexingRequest) (*emptypb.Empty, error) {
	caller, ok := middleware.CallerFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "missing verified caller context")
	}
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}

	_, err := s.abortCmd.Execute(ctx, dto.AbortReindexingInput{
		Caller:          caller,
		KBID:            req.KbId,
		ExpectedVersion: req.ExpectedVersion,
		Reason:          req.Reason,
	})
	if err != nil {
		return nil, interceptors.MapDomainErrorToGRPC(err)
	}
	return &emptypb.Empty{}, nil
}

func fromProtoVectorSchema(vs *catalogv1.VectorSchema) entity.VectorSchema {
	if vs == nil {
		return entity.VectorSchema{}
	}
	var metric entity.DistanceMetric
	switch vs.DistanceMetric {
	case catalogv1.DistanceMetric_DISTANCE_METRIC_COSINE:
		metric = entity.MetricCosine
	case catalogv1.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT:
		metric = entity.MetricDotProduct
	case catalogv1.DistanceMetric_DISTANCE_METRIC_L2:
		metric = entity.MetricL2
	}
	return entity.VectorSchema{
		EmbeddingModel: vs.EmbeddingModel,
		Dimensions:     vs.Dimensions,
		DistanceMetric: metric,
		SchemaVersion:  vs.SchemaVersion,
	}
}

func toProtoVectorSchema(vs entity.VectorSchema) *catalogv1.VectorSchema {
	var metric catalogv1.DistanceMetric
	switch vs.DistanceMetric {
	case entity.MetricCosine:
		metric = catalogv1.DistanceMetric_DISTANCE_METRIC_COSINE
	case entity.MetricDotProduct:
		metric = catalogv1.DistanceMetric_DISTANCE_METRIC_DOT_PRODUCT
	case entity.MetricL2:
		metric = catalogv1.DistanceMetric_DISTANCE_METRIC_L2
	}
	return &catalogv1.VectorSchema{
		EmbeddingModel: vs.EmbeddingModel,
		Dimensions:     vs.Dimensions,
		DistanceMetric: metric,
		SchemaVersion:  vs.SchemaVersion,
	}
}

func fromProtoRetrievalProfile(rp *catalogv1.RetrievalProfile) entity.RetrievalProfile {
	if rp == nil {
		return entity.RetrievalProfile{}
	}
	return entity.RetrievalProfile{
		DefaultTopK:                     rp.DefaultTopK,
		DenseWeight:                     rp.DenseWeight,
		SparseWeight:                    rp.SparseWeight,
		RerankerCandidatePoolSize:       rp.RerankerCandidatePoolSize,
		CrossEncoderConfidenceThreshold: rp.CrossEncoderConfidenceThreshold,
	}
}

func toProtoRetrievalProfile(rp entity.RetrievalProfile) *catalogv1.RetrievalProfile {
	return &catalogv1.RetrievalProfile{
		DefaultTopK:                     rp.DefaultTopK,
		DenseWeight:                     rp.DenseWeight,
		SparseWeight:                    rp.SparseWeight,
		RerankerCandidatePoolSize:       rp.RerankerCandidatePoolSize,
		CrossEncoderConfidenceThreshold: rp.CrossEncoderConfidenceThreshold,
	}
}

func toProtoLifecycleState(s entity.LifecycleState) catalogv1.LifecycleState {
	switch s {
	case entity.StateCreating:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_CREATING
	case entity.StateActive:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_ACTIVE
	case entity.StateReindexing:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_REINDEXING
	case entity.StatePromoted:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_PROMOTED
	case entity.StateAborted:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_ABORTED
	case entity.StateTombstoned:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_TOMBSTONED
	default:
		return catalogv1.LifecycleState_LIFECYCLE_STATE_UNSPECIFIED
	}
}

func toProtoRoutingPolicy(r entity.RoutingPolicy) catalogv1.RoutingPolicy {
	switch r {
	case entity.RoutingGreenOnly:
		return catalogv1.RoutingPolicy_ROUTING_POLICY_GREEN_ONLY
	case entity.RoutingDualWrite:
		return catalogv1.RoutingPolicy_ROUTING_POLICY_DUAL_WRITE
	default:
		return catalogv1.RoutingPolicy_ROUTING_POLICY_UNSPECIFIED
	}
}

func toProtoKB(kb *entity.KnowledgeBase) *catalogv1.KnowledgeBase {
	if kb == nil {
		return nil
	}
	var candidateSchema *catalogv1.VectorSchema
	if kb.CandidateSchema != nil {
		candidateSchema = toProtoVectorSchema(*kb.CandidateSchema)
	}
	return &catalogv1.KnowledgeBase{
		TenantId:              kb.TenantID,
		KbId:                  kb.KBID,
		Name:                  kb.Name,
		Description:           kb.Description,
		Version:               kb.Version,
		State:                 toProtoLifecycleState(kb.State),
		RoutingPolicy:         toProtoRoutingPolicy(kb.RoutingPolicy),
		ActiveIndexPointer:    kb.ActiveIndexPointer,
		CandidateIndexPointer: kb.CandidateIndexPointer,
		ActiveSchema:          toProtoVectorSchema(kb.ActiveSchema),
		CandidateSchema:       candidateSchema,
		RetrievalProfile:      toProtoRetrievalProfile(kb.RetrievalProfile),
		TaxonomyTags:          kb.TaxonomyTags,
		CreatedAt:             timestamppb.New(kb.CreatedAt),
		UpdatedAt:             timestamppb.New(kb.UpdatedAt),
	}
}
