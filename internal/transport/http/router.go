package httptransport

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/http/middleware"
)

// MetricsHandler returns the Prometheus scrape handler based on the configured metrics registry.
func MetricsHandler(metrics *observability.Metrics) http.Handler {
	if metrics != nil && metrics.Registry != nil {
		return promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{})
	}
	return promhttp.Handler()
}

// NewMetricsRouter constructs an internal HTTP router dedicated to serving /metrics on an isolated internal listener.
func NewMetricsRouter(metrics *observability.Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler(metrics))
	return mux
}

// NewRouter constructs the HTTP ServeMux wiring OpenAPI 3.1 Doc API routes, operational probes,
// and JWT-authenticated /v1/knowledge-bases routes. Public router protects or isolates /metrics.
func NewRouter(
	h *Handler,
	jwtSecret string,
	logger *slog.Logger,
	metrics *observability.Metrics,
) http.Handler {
	mux := http.NewServeMux()

	// Operational Probes & Doc API Endpoints (Unauthenticated)
	mux.HandleFunc("GET /healthz", h.Healthz)
	mux.HandleFunc("GET /ready", h.Ready)
	mux.HandleFunc("GET /docs", h.ServeSwaggerUI)
	mux.HandleFunc("GET /redoc", h.ServeReDocUI)
	mux.HandleFunc("GET /openapi.json", h.ServeOpenAPIJSON)
	mux.HandleFunc("GET /api/docs", h.ServeOpenAPIJSON)

	// Authenticated /v1/knowledge-bases Administrative & Discovery Routes
	authWrap := middleware.AuthMiddleware(jwtSecret, WriteError)

	// Protect /metrics on the public mux with authWrap to prevent public unauthenticated exposure
	mux.Handle("GET /metrics", authWrap(MetricsHandler(metrics)))

	v1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.RequestIDFromContext(r.Context())
		path := strings.TrimSuffix(r.URL.Path, "/")

		if path == "/v1/knowledge-bases" {
			*r = *middleware.WithRoutePattern(r, "/v1/knowledge-bases")
			switch r.Method {
			case http.MethodPost:
				h.CreateKnowledgeBase(w, r)
			case http.MethodGet:
				h.ListKnowledgeBases(w, r)
			default:
				WriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", reqID)
			}
			return
		}

		const prefix = "/v1/knowledge-bases/"
		if !strings.HasPrefix(path, prefix) {
			*r = *middleware.WithRoutePattern(r, "unmatched")
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "route not found", reqID)
			return
		}

		rest := strings.TrimPrefix(path, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			*r = *middleware.WithRoutePattern(r, "unmatched")
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "route not found", reqID)
			return
		}

		// Handle custom Blue-Green action routes (:triggerReindexing, :promotePointer, :abortReindexing)
		if colonIdx := strings.IndexByte(rest, ':'); colonIdx != -1 {
			kbID := strings.TrimSpace(rest[:colonIdx])
			action := rest[colonIdx+1:]
			pattern := "/v1/knowledge-bases/{kb_id}:" + action
			*r = *middleware.WithRoutePattern(r, pattern)
			if kbID == "" {
				WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "kb_id is required", reqID)
				return
			}
			if r.Method != http.MethodPost {
				WriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "action routes require POST", reqID)
				return
			}
			switch action {
			case "triggerReindexing":
				h.TriggerReindexing(w, r, kbID)
			case "promotePointer":
				h.PromotePointer(w, r, kbID)
			case "abortReindexing":
				h.AbortReindexing(w, r, kbID)
			default:
				*r = *middleware.WithRoutePattern(r, "unmatched")
				WriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown action route", reqID)
			}
			return
		}

		*r = *middleware.WithRoutePattern(r, "/v1/knowledge-bases/{kb_id}")
		kbID := strings.TrimSpace(rest)
		switch r.Method {
		case http.MethodGet:
			h.GetKnowledgeBase(w, r, kbID)
		case http.MethodPatch:
			h.UpdateKnowledgeBase(w, r, kbID)
		case http.MethodDelete:
			h.DeleteKnowledgeBase(w, r, kbID)
		default:
			WriteError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", reqID)
		}
	})

	mux.Handle("/v1/knowledge-bases", authWrap(v1Handler))
	mux.Handle("/v1/knowledge-bases/", authWrap(v1Handler))

	var root http.Handler = mux
	root = middleware.LoggingAndMetricsMiddleware(logger, metrics)(root)
	root = middleware.RequestIDMiddleware(root)
	return root
}
