package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/dto"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/http/middleware"
)

// ReadinessCheckFunc evaluates upstream PostgreSQL and Redis Cluster connectivity for GET /ready.
type ReadinessCheckFunc func(ctx context.Context) (postgresConnected bool, redisConnected bool, err error)

// Handler serves the OpenAPI 3.1 administrative REST endpoints, Doc API UI routes, and operational probes.
type Handler struct {
	createCmd      *command.CreateKnowledgeBaseCommand
	updateCmd      *command.UpdateKnowledgeBaseCommand
	triggerCmd     *command.TriggerReindexingCommand
	promoteCmd     *command.PromotePointerCommand
	abortCmd       *command.AbortReindexingCommand
	deleteCmd      *command.DeleteKnowledgeBaseCommand
	getQuery       *query.GetKnowledgeBaseQuery
	listQuery      *query.ListKnowledgeBasesQuery
	readinessCheck ReadinessCheckFunc
	openAPIJSON    []byte
}

// NewHandler constructs an HTTP Handler with all command and query dependencies.
func NewHandler(
	createCmd *command.CreateKnowledgeBaseCommand,
	updateCmd *command.UpdateKnowledgeBaseCommand,
	triggerCmd *command.TriggerReindexingCommand,
	promoteCmd *command.PromotePointerCommand,
	abortCmd *command.AbortReindexingCommand,
	deleteCmd *command.DeleteKnowledgeBaseCommand,
	getQuery *query.GetKnowledgeBaseQuery,
	listQuery *query.ListKnowledgeBasesQuery,
	readinessCheck ReadinessCheckFunc,
	openAPIJSON []byte,
) *Handler {
	if len(openAPIJSON) == 0 {
		openAPIJSON = []byte(defaultOpenAPISpecJSON)
	}
	return &Handler{
		createCmd:      createCmd,
		updateCmd:      updateCmd,
		triggerCmd:     triggerCmd,
		promoteCmd:     promoteCmd,
		abortCmd:       abortCmd,
		deleteCmd:      deleteCmd,
		getQuery:       getQuery,
		listQuery:      listQuery,
		readinessCheck: readinessCheck,
		openAPIJSON:    openAPIJSON,
	}
}

// Healthz handles GET /healthz liveness probe.
func (h *Handler) Healthz(w http.ResponseWriter, _ *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]string{"status": "healthy"})
}

// Ready handles GET /ready readiness probe verifying PostgreSQL pool and Redis Cluster connectivity.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	reqID := middleware.RequestIDFromContext(r.Context())
	redisStatus := "connected"
	if h.readinessCheck != nil {
		pgOK, redisOK, err := h.readinessCheck(r.Context())
		if err != nil || !pgOK {
			WriteError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "upstream dependency unreachable", reqID)
			return
		}
		if !redisOK {
			redisStatus = "disabled"
		}
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"status":        "ready",
		"postgres":      "connected",
		"redis_cluster": redisStatus,
	})
}

// ServeOpenAPIJSON handles GET /openapi.json and GET /api/docs.
func (h *Handler) ServeOpenAPIJSON(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(h.openAPIJSON)
}

// ServeSwaggerUI handles GET /docs.
func (h *Handler) ServeSwaggerUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(swaggerUIHTML))
}

// ServeReDocUI handles GET /redoc.
func (h *Handler) ServeReDocUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(redocUIHTML))
}

const maxRequestBodyBytes = 1 << 20 // 1 MiB upper bound

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	return dec.Decode(dst)
}

// CreateKnowledgeBase handles POST /v1/knowledge-bases.
func (h *Handler) CreateKnowledgeBase(w http.ResponseWriter, r *http.Request) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var in dto.CreateKBInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}
	in.Caller = caller
	in.HeaderIdempKey = strings.TrimSpace(r.Header.Get("Idempotency-Key"))

	resp, err := h.createCmd.Execute(r.Context(), in)
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusCreated, resp)
}

// ListKnowledgeBases handles GET /v1/knowledge-bases.
func (h *Handler) ListKnowledgeBases(w http.ResponseWriter, r *http.Request) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	q := r.URL.Query()
	var filters []string
	for _, raw := range q["taxonomy_filter"] {
		for _, part := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				filters = append(filters, trimmed)
			}
		}
	}

	pageSize := 20
	if psStr := strings.TrimSpace(q.Get("page_size")); psStr != "" {
		ps, err := strconv.Atoi(psStr)
		if err != nil || ps < 1 || ps > 100 {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "page_size must be an integer between 1 and 100", reqID)
			return
		}
		pageSize = ps
	}

	resp, err := h.listQuery.Execute(r.Context(), dto.ListKBsInput{
		Caller:          caller,
		TaxonomyFilters: filters,
		PageSize:        pageSize,
		PageToken:       strings.TrimSpace(q.Get("page_token")),
	})
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

// GetKnowledgeBase handles GET /v1/knowledge-bases/{kb_id}.
func (h *Handler) GetKnowledgeBase(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var minVer uint64
	if mvStr := strings.TrimSpace(r.URL.Query().Get("min_version")); mvStr != "" {
		parsed, err := strconv.ParseUint(mvStr, 10, 64)
		if err != nil || parsed == 0 {
			WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "min_version must be an integer >= 1", reqID)
			return
		}
		minVer = parsed
	}

	resp, err := h.getQuery.Execute(r.Context(), dto.GetKBInput{
		Caller:     caller,
		KBID:       kbID,
		MinVersion: minVer,
	})
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

// UpdateKnowledgeBase handles PATCH /v1/knowledge-bases/{kb_id}.
func (h *Handler) UpdateKnowledgeBase(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var rawMap map[string]json.RawMessage
	if err := decodeJSONBody(w, r, &rawMap); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}

	bodyBytes, err := json.Marshal(rawMap)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}

	var in dto.UpdateKBInput
	if err := json.Unmarshal(bodyBytes, &in); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid update request fields", reqID)
		return
	}

	// Detect if taxonomy_tags was explicitly included in the JSON payload (including empty array []).
	if _, hasTags := rawMap["taxonomy_tags"]; hasTags {
		in.TaxonomyTagsPresent = true
	}

	// Catch attempts to mutate active_schema or vector_schema directly in the REST PATCH body.
	// Check active_schema and vector_schema independently so one does not hide the other.
	if in.AttemptedSchemaMutation == nil {
		if rawSchema, ok := rawMap["active_schema"]; ok && len(rawSchema) > 0 && string(rawSchema) != "null" {
			var s entity.VectorSchema
			if err := json.Unmarshal(rawSchema, &s); err == nil {
				in.AttemptedSchemaMutation = &s
			} else {
				// Non-unmarshalable value still indicates a mutation attempt on active_schema
				in.AttemptedSchemaMutation = &entity.VectorSchema{EmbeddingModel: "__invalid_schema_mutation__"}
			}
		}
	}
	if in.AttemptedSchemaMutation == nil {
		if rawVecSchema, ok := rawMap["vector_schema"]; ok && len(rawVecSchema) > 0 && string(rawVecSchema) != "null" {
			var s entity.VectorSchema
			if err := json.Unmarshal(rawVecSchema, &s); err == nil {
				in.AttemptedSchemaMutation = &s
			} else {
				// Non-unmarshalable value still indicates a mutation attempt on vector_schema
				in.AttemptedSchemaMutation = &entity.VectorSchema{EmbeddingModel: "__invalid_schema_mutation__"}
			}
		}
	}

	in.Caller = caller
	in.KBID = kbID

	resp, err := h.updateCmd.Execute(r.Context(), in)
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

// DeleteKnowledgeBase handles DELETE /v1/knowledge-bases/{kb_id}.
func (h *Handler) DeleteKnowledgeBase(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	evStr := strings.TrimSpace(r.URL.Query().Get("expected_version"))
	expectedVer, err := strconv.ParseUint(evStr, 10, 64)
	if err != nil || expectedVer == 0 {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "expected_version query parameter (>= 1) is required", reqID)
		return
	}

	err = h.deleteCmd.Execute(r.Context(), dto.DeleteKBInput{
		Caller:          caller,
		KBID:            kbID,
		ExpectedVersion: expectedVer,
	})
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TriggerReindexing handles POST /v1/knowledge-bases/{kb_id}:triggerReindexing.
func (h *Handler) TriggerReindexing(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var in dto.TriggerReindexingInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}
	in.Caller = caller
	in.KBID = kbID

	resp, err := h.triggerCmd.Execute(r.Context(), in)
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusAccepted, resp)
}

// PromotePointer handles POST /v1/knowledge-bases/{kb_id}:promotePointer.
func (h *Handler) PromotePointer(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var in dto.PromotePointerInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}
	in.Caller = caller
	in.KBID = kbID

	resp, err := h.promoteCmd.Execute(r.Context(), in)
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

// AbortReindexing handles POST /v1/knowledge-bases/{kb_id}:abortReindexing.
func (h *Handler) AbortReindexing(w http.ResponseWriter, r *http.Request, kbID string) {
	reqID := middleware.RequestIDFromContext(r.Context())
	caller, ok := middleware.CallerFromContext(r.Context())
	if !ok {
		WriteError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "missing caller identity", reqID)
		return
	}

	var in dto.AbortReindexingInput
	if err := decodeJSONBody(w, r, &in); err != nil {
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "malformed JSON request body", reqID)
		return
	}
	in.Caller = caller
	in.KBID = kbID

	resp, err := h.abortCmd.Execute(r.Context(), in)
	if err != nil {
		h.handleDomainError(w, err, reqID)
		return
	}
	WriteJSON(w, http.StatusOK, resp)
}

func (h *Handler) handleDomainError(w http.ResponseWriter, err error, reqID string) {
	switch {
	case errors.Is(err, entity.ErrStructuralMutationDisallowed):
		WriteError(w, http.StatusUnprocessableEntity, "STRUCTURAL_MUTATION_DISALLOWED", err.Error(), reqID)
	case errors.Is(err, entity.ErrVersionConflict):
		WriteError(w, http.StatusConflict, "VERSION_CONFLICT", err.Error(), reqID)
	case errors.Is(err, entity.ErrDeltaLagNotZero):
		WriteError(w, http.StatusConflict, "DELTA_LAG_NOT_ZERO", err.Error(), reqID)
	case errors.Is(err, entity.ErrInvalidStateTransition):
		WriteError(w, http.StatusConflict, "INVALID_STATE_TRANSITION", err.Error(), reqID)
	case errors.Is(err, entity.ErrDuplicateKBName):
		WriteError(w, http.StatusConflict, "DUPLICATE_KB_NAME", err.Error(), reqID)
	case errors.Is(err, entity.ErrIdempotencyKeyConflict):
		WriteError(w, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", err.Error(), reqID)
	case errors.Is(err, entity.ErrKnowledgeBaseNotFound):
		WriteError(w, http.StatusNotFound, "NOT_FOUND", err.Error(), reqID)
	case errors.Is(err, entity.ErrInsufficientPermissions), errors.Is(err, entity.ErrTenantMismatch):
		WriteError(w, http.StatusForbidden, "PERMISSION_DENIED", err.Error(), reqID)
	case errors.Is(err, entity.ErrInvalidInput):
		WriteError(w, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), reqID)
	default:
		WriteError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", reqID)
	}
}

// WriteJSON serializes payload using a pooled bytes.Buffer (sync.Pool) to maintain flat RSS <= 128 MiB.
func WriteJSON(w http.ResponseWriter, statusCode int, payload any) {
	buf := observability.AcquireBuffer()
	defer observability.ReleaseBuffer(buf)

	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"code":"INTERNAL_ERROR","message":"failed to encode response"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_, _ = w.Write(buf.Bytes())
}

// WriteError writes a sanitized ErrorResponse JSON payload.
func WriteError(w http.ResponseWriter, statusCode int, code, message, reqID string) {
	WriteJSON(w, statusCode, map[string]string{
		"code":       code,
		"message":    message,
		"request_id": reqID,
	})
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>RAG KB Catalog Service - Swagger UI</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      window.ui = SwaggerUIBundle({ url: '/openapi.json', dom_id: '#swagger-ui' });
    };
  </script>
</body>
</html>`

const redocUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8" />
  <title>RAG KB Catalog Service - ReDoc</title>
</head>
<body>
  <redoc spec-url="/openapi.json"></redoc>
  <script src="https://cdn.redoc.ly/redoc/latest/bundles/redoc.standalone.js"></script>
</body>
</html>`

const defaultOpenAPISpecJSON = `{
  "openapi": "3.1.0",
  "info": {
    "title": "RAG Knowledge Base Catalog Service API",
    "version": "1.0.0"
  },
  "paths": {
    "/healthz": {},
    "/ready": {},
    "/v1/knowledge-bases": {},
    "/v1/knowledge-bases/{kb_id}": {},
    "/v1/knowledge-bases/{kb_id}:triggerReindexing": {},
    "/v1/knowledge-bases/{kb_id}:promotePointer": {},
    "/v1/knowledge-bases/{kb_id}:abortReindexing": {}
  }
}`
