package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
)

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.statusCode = code
	sr.ResponseWriter.WriteHeader(code)
}

// RoutePatternContextKey is the context key for custom derived route labels.
type routePatternContextKey struct{}

// RoutePatternKey is the key instance used to store route patterns.
var RoutePatternKey = routePatternContextKey{}

// RequestMetadata holds mutable per-request telemetry metadata (route pattern, tenant ID)
// shared across derived context boundaries.
type RequestMetadata struct {
	RoutePattern string
	TenantID     string
}

type requestMetadataKey struct{}

// WithRequestMetadata injects a mutable RequestMetadata holder into the context.
func WithRequestMetadata(ctx context.Context, meta *RequestMetadata) context.Context {
	return context.WithValue(ctx, requestMetadataKey{}, meta)
}

// RequestMetadataFromContext retrieves the mutable RequestMetadata holder from context.
func RequestMetadataFromContext(ctx context.Context) *RequestMetadata {
	if meta, ok := ctx.Value(requestMetadataKey{}).(*RequestMetadata); ok && meta != nil {
		return meta
	}
	return nil
}

// WithRoutePattern stores a bounded route pattern in the request context and shared metadata holder.
func WithRoutePattern(r *http.Request, pattern string) *http.Request {
	if meta := RequestMetadataFromContext(r.Context()); meta != nil {
		meta.RoutePattern = pattern
	}
	return r.WithContext(context.WithValue(r.Context(), RoutePatternKey, pattern))
}

// GetRoutePattern retrieves the route pattern from the request.
func GetRoutePattern(r *http.Request) string {
	if v, ok := r.Context().Value(RoutePatternKey).(string); ok && v != "" {
		return v
	}
	if r.Pattern != "" {
		return r.Pattern
	}
	return "unmatched"
}

// LoggingAndMetricsMiddleware records Prometheus request counters/histograms, recovers panics,
// and emits structured JSON logs via log/slog with mandatory correlation fields and redaction.
func LoggingAndMetricsMiddleware(logger *slog.Logger, metrics *observability.Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{
				ResponseWriter: w,
				statusCode:     http.StatusOK,
			}
			meta := &RequestMetadata{}
			reqWithMeta := r.WithContext(WithRequestMetadata(r.Context(), meta))

			defer func() {
				if recovered := recover(); recovered != nil {
					rec.statusCode = http.StatusInternalServerError
					if logger != nil {
						logger.Error("http handler panic recovered",
							slog.String("request_id", RequestIDFromContext(reqWithMeta.Context())),
							slog.String("method", reqWithMeta.Method+" "+reqWithMeta.URL.Path),
						)
					}
					http.Error(w, `{"code":"INTERNAL_ERROR","message":"internal server error"}`, http.StatusInternalServerError)
				}

				duration := time.Since(start)
				tenantID := meta.TenantID
				if tenantID == "" {
					caller, _ := CallerFromContext(reqWithMeta.Context())
					tenantID = caller.TenantID
				}
				if tenantID == "" {
					tenantID = "anonymous"
				}

				pattern := meta.RoutePattern
				if pattern == "" {
					pattern = GetRoutePattern(reqWithMeta)
				}
				methodLabel := reqWithMeta.Method + " " + pattern
				statusStr := strconv.Itoa(rec.statusCode)

				if metrics != nil {
					metrics.RequestsTotal.WithLabelValues(methodLabel, tenantID, statusStr).Inc()
					metrics.RequestDurationSeconds.WithLabelValues(methodLabel, "http").Observe(duration.Seconds())
				}

				if logger != nil {
					logger.Info("http request completed",
						slog.String("request_id", RequestIDFromContext(reqWithMeta.Context())),
						slog.String("tenant_id", tenantID),
						slog.String("method", methodLabel),
						slog.String("path", reqWithMeta.URL.Path),
						slog.Int("status_code", rec.statusCode),
						slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
					)
				}
			}()

			next.ServeHTTP(rec, reqWithMeta)
		})
	}
}
