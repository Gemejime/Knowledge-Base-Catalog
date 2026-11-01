package middleware

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gemejime/rag-kb-catalog-service/internal/domain/entity"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/policy"
)

type contextKey string

const (
	callerContextKey    contextKey = "rag_caller_identity"
	requestIDContextKey contextKey = "rag_request_id"
)

// ErrUnauthenticated indicates that no valid caller authentication context was provided.
var ErrUnauthenticated = errors.New("missing or invalid authentication credentials")

// WithCallerIdentity injects a verified policy.CallerIdentity into the context.
func WithCallerIdentity(ctx context.Context, caller policy.CallerIdentity) context.Context {
	if caller.RequestID != "" {
		ctx = context.WithValue(ctx, requestIDContextKey, caller.RequestID)
	}
	return context.WithValue(ctx, callerContextKey, caller)
}

// CallerFromContext retrieves the verified policy.CallerIdentity from the context.
func CallerFromContext(ctx context.Context) (policy.CallerIdentity, bool) {
	c, ok := ctx.Value(callerContextKey).(policy.CallerIdentity)
	return c, ok
}

// WithRequestID attaches a correlation request_id to the context.
func WithRequestID(ctx context.Context, reqID string) context.Context {
	return context.WithValue(ctx, requestIDContextKey, reqID)
}

// RequestIDFromContext returns the correlation request_id from the context.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDContextKey).(string); ok && v != "" {
		return v
	}
	if c, ok := CallerFromContext(ctx); ok && c.RequestID != "" {
		return c.RequestID
	}
	return ""
}

// RequestIDMiddleware injects or propagates X-Request-ID on every HTTP request and response.
func RequestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if reqID == "" {
			var err error
			reqID, err = entity.NewUUIDv4()
			if err != nil {
				reqID = "req-fallback-id"
			}
		}
		w.Header().Set("X-Request-ID", reqID)
		ctx := WithRequestID(r.Context(), reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AuthMiddleware extracts and validates caller JWT claims (or Edge-Gateway verified headers),
// mapping incoming "sub"/"subject" to canonical "user_id" alongside "tenant_id" and "roles".
func AuthMiddleware(jwtSecret string, writeError func(w http.ResponseWriter, status int, code, msg, reqID string)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())
			caller, err := ExtractHTTPIdentity(r, jwtSecret, reqID)
			if err != nil {
				writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", err.Error(), reqID)
				return
			}
			ctx := WithCallerIdentity(r.Context(), caller)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ExtractHTTPIdentity parses the caller identity from a Bearer JWT token or Edge-Gateway propagated headers.
func ExtractHTTPIdentity(r *http.Request, jwtSecret, reqID string) (policy.CallerIdentity, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			return policy.CallerIdentity{}, ErrUnauthenticated
		}
		rawToken := strings.TrimSpace(parts[1])
		caller, err := ParseAndVerifyJWT(rawToken, jwtSecret, reqID)
		if err != nil {
			return policy.CallerIdentity{}, err
		}
		return caller, nil
	}

	// Fallback to Edge-Gateway pre-verified headers when propagated inside the service mesh.
	tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
	userID := strings.TrimSpace(r.Header.Get("X-User-ID"))
	if userID == "" {
		userID = strings.TrimSpace(r.Header.Get("X-Subject"))
	}
	rolesHeader := strings.TrimSpace(r.Header.Get("X-Roles"))
	if tenantID == "" || userID == "" {
		return policy.CallerIdentity{}, ErrUnauthenticated
	}

	var roles []policy.Role
	if rolesHeader != "" {
		for _, tok := range strings.Split(rolesHeader, ",") {
			trimmed := strings.TrimSpace(tok)
			if trimmed != "" {
				roles = append(roles, policy.Role(trimmed))
			}
		}
	}

	return policy.CallerIdentity{
		TenantID:  tenantID,
		UserID:    userID,
		Roles:     roles,
		RequestID: reqID,
	}, nil
}

type jwtClaims struct {
	TenantID string   `json:"tenant_id"`
	Sub      string   `json:"sub"`
	Subject  string   `json:"subject"`
	UserID   string   `json:"user_id"`
	Roles    []string `json:"roles"`
	Role     string   `json:"role"`
}

// ParseAndVerifyJWT decodes a JWT token, verifies its HMAC-SHA256 signature when jwtSecret is non-empty,
// and maps incoming "sub"/"subject" to canonical "user_id".
func ParseAndVerifyJWT(tokenStr, jwtSecret, reqID string) (policy.CallerIdentity, error) {
	segments := strings.Split(tokenStr, ".")
	if len(segments) != 3 {
		return policy.CallerIdentity{}, ErrUnauthenticated
	}

	if jwtSecret != "" {
		signingInput := segments[0] + "." + segments[1]
		mac := hmac.New(sha256.New, []byte(jwtSecret))
		_, _ = mac.Write([]byte(signingInput))
		expectedSig := mac.Sum(nil)
		actualSig, err := base64.RawURLEncoding.DecodeString(segments[2])
		if err != nil || !hmac.Equal(actualSig, expectedSig) {
			return policy.CallerIdentity{}, ErrUnauthenticated
		}
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(segments[1])
	if err != nil {
		return policy.CallerIdentity{}, ErrUnauthenticated
	}

	var claims jwtClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return policy.CallerIdentity{}, ErrUnauthenticated
	}

	tenantID := strings.TrimSpace(claims.TenantID)
	// Map incoming sub / subject to canonical user_id before access_evaluator.go executes.
	userID := strings.TrimSpace(claims.Sub)
	if userID == "" {
		userID = strings.TrimSpace(claims.Subject)
	}
	if userID == "" {
		userID = strings.TrimSpace(claims.UserID)
	}
	if tenantID == "" || userID == "" {
		return policy.CallerIdentity{}, ErrUnauthenticated
	}

	var roles []policy.Role
	for _, r := range claims.Roles {
		if trimmed := strings.TrimSpace(r); trimmed != "" {
			roles = append(roles, policy.Role(trimmed))
		}
	}
	if claims.Role != "" {
		if trimmed := strings.TrimSpace(claims.Role); trimmed != "" {
			roles = append(roles, policy.Role(trimmed))
		}
	}

	return policy.CallerIdentity{
		TenantID:  tenantID,
		UserID:    userID,
		Roles:     roles,
		RequestID: reqID,
	}, nil
}
