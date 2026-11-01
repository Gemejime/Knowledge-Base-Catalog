package interceptors

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/http/middleware"
)

type tenantScopedRequest interface {
	GetTenantId() string
}

// UnaryAuthLoggingInterceptor extracts caller identity from gRPC metadata (Bearer JWT or verified Edge-Gateway headers),
// enforces tenant spoofing defense (F-09), records Prometheus telemetry, emits structured log/slog JSON entries,
// and maps domain sentinel errors deterministically to gRPC status codes.
func UnaryAuthLoggingInterceptor(
	jwtSecret string,
	logger *slog.Logger,
	metrics *observability.Metrics,
) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		start := time.Now()
		md, _ := metadata.FromIncomingContext(ctx)

		reqID := firstMD(md, "x-request-id")
		if reqID == "" {
			reqID, _ = entity.NewUUIDv4()
		}

		caller, authErr := extractGRPCIdentity(md, jwtSecret, reqID)
		if authErr != nil {
			recordGRPCTelemetry(logger, metrics, info.FullMethod, "anonymous", reqID, codes.Unauthenticated, time.Since(start))
			return nil, status.Error(codes.Unauthenticated, authErr.Error())
		}

		// F-09: Defend against gRPC payload tenant_id spoofing.
		if tReq, ok := req.(tenantScopedRequest); ok {
			payloadTenant := strings.TrimSpace(tReq.GetTenantId())
			if payloadTenant != "" && payloadTenant != caller.TenantID {
				recordGRPCTelemetry(logger, metrics, info.FullMethod, caller.TenantID, reqID, codes.PermissionDenied, time.Since(start))
				return nil, status.Error(codes.PermissionDenied, entity.ErrTenantMismatch.Error())
			}
		}

		ctx = middleware.WithCallerIdentity(ctx, caller)
		resp, err = handler(ctx, req)
		if err != nil {
			mappedErr := MapDomainErrorToGRPC(err)
			st, _ := status.FromError(mappedErr)
			recordGRPCTelemetry(logger, metrics, info.FullMethod, caller.TenantID, reqID, st.Code(), time.Since(start))
			return nil, mappedErr
		}

		recordGRPCTelemetry(logger, metrics, info.FullMethod, caller.TenantID, reqID, codes.OK, time.Since(start))
		return resp, nil
	}
}

func extractGRPCIdentity(md metadata.MD, jwtSecret, reqID string) (policy.CallerIdentity, error) {
	authVal := firstMD(md, "authorization")
	if authVal == "" {
		return policy.CallerIdentity{}, middleware.ErrUnauthenticated
	}

	parts := strings.SplitN(authVal, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return policy.CallerIdentity{}, middleware.ErrUnauthenticated
	}
	return middleware.ParseAndVerifyJWT(strings.TrimSpace(parts[1]), jwtSecret, reqID)
}

func firstMD(md metadata.MD, key string) string {
	vals := md.Get(key)
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimSpace(vals[0])
}

func recordGRPCTelemetry(
	logger *slog.Logger,
	metrics *observability.Metrics,
	method string,
	tenantID string,
	reqID string,
	code codes.Code,
	duration time.Duration,
) {
	if metrics != nil {
		metrics.RequestsTotal.WithLabelValues(method, tenantID, code.String()).Inc()
		metrics.RequestDurationSeconds.WithLabelValues(method, "grpc").Observe(duration.Seconds())
	}
	if logger != nil {
		logger.Info("grpc request completed",
			slog.String("request_id", reqID),
			slog.String("tenant_id", tenantID),
			slog.String("method", method),
			slog.String("status_code", code.String()),
			slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
		)
	}
}

// MapDomainErrorToGRPC maps domain sentinel errors deterministically to gRPC status codes.
func MapDomainErrorToGRPC(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, entity.ErrStructuralMutationDisallowed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrVersionConflict):
		return status.Error(codes.Aborted, err.Error())
	case errors.Is(err, entity.ErrDeltaLagNotZero):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrInvalidStateTransition):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrDuplicateKBName), errors.Is(err, entity.ErrIdempotencyKeyConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, entity.ErrKnowledgeBaseNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, entity.ErrInsufficientPermissions), errors.Is(err, entity.ErrTenantMismatch):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, entity.ErrInvalidInput):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal server error")
	}
}
