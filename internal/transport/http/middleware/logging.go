package middleware

import (
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

			defer func() {
				if recovered := recover(); recovered != nil {
					rec.statusCode = http.StatusInternalServerError
					if logger != nil {
						logger.Error("http handler panic recovered",
							slog.String("request_id", RequestIDFromContext(r.Context())),
							slog.String("method", r.Method+" "+r.URL.Path),
						)
					}
					http.Error(w, `{"code":"INTERNAL_ERROR","message":"internal server error"}`, http.StatusInternalServerError)
				}

				duration := time.Since(start)
				caller, _ := CallerFromContext(r.Context())
				tenantID := caller.TenantID
				if tenantID == "" {
					tenantID = "anonymous"
				}
				methodLabel := r.Method + " " + r.URL.Path
				statusStr := strconv.Itoa(rec.statusCode)

				if metrics != nil {
					metrics.RequestsTotal.WithLabelValues(methodLabel, tenantID, statusStr).Inc()
					metrics.RequestDurationSeconds.WithLabelValues(methodLabel, "http").Observe(duration.Seconds())
				}

				if logger != nil {
					logger.Info("http request completed",
						slog.String("request_id", RequestIDFromContext(r.Context())),
						slog.String("tenant_id", tenantID),
						slog.String("method", methodLabel),
						slog.Int("status_code", rec.statusCode),
						slog.Float64("duration_ms", float64(duration.Microseconds())/1000.0),
					)
				}
			}()

			next.ServeHTTP(rec, r)
		})
	}
}
