package httptransport

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/http/middleware"
)

// NewRouter constructs the HTTP ServeMux wiring OpenAPI 3.1 Doc API routes, operational probes,
// Prometheus /metrics scrape endpoint, and JWT-authenticated /v1/knowledge-bases routes.
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

	if metrics != nil && metrics.Registry != nil {
		mux.Handle("GET /metrics", promhttp.HandlerFor(metrics.Registry, promhttp.HandlerOpts{}))
	} else {
		mux.Handle("GET /metrics", promhttp.Handler())
	}

	// Authenticated /v1/knowledge-bases Administrative & Discovery Routes
	authWrap := middleware.AuthMiddleware(jwtSecret, WriteError)

	v1Handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := middleware.RequestIDFromContext(r.Context())
		path := strings.TrimSuffix(r.URL.Path, "/")

		if path == "/v1/knowledge-bases" {
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
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "route not found", reqID)
			return
		}

		rest := strings.TrimPrefix(path, prefix)
		if rest == "" || strings.Contains(rest, "/") {
			WriteError(w, http.StatusNotFound, "NOT_FOUND", "route not found", reqID)
			return
		}

		// Handle custom Blue-Green action routes (:triggerReindexing, :promotePointer, :abortReindexing)
		if colonIdx := strings.IndexByte(rest, ':'); colonIdx != -1 {
			kbID := strings.TrimSpace(rest[:colonIdx])
			action := rest[colonIdx+1:]
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
				WriteError(w, http.StatusNotFound, "NOT_FOUND", "unknown action route", reqID)
			}
			return
		}

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
