package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// RedactedValue is the canonical replacement string for sensitive credentials, tokens, and DSNs.
const RedactedValue = "[REDACTED]"

var sensitiveKeys = map[string]struct{}{
	"authorization":  {},
	"bearer":         {},
	"token":          {},
	"jwt_token":      {},
	"raw_token":      {},
	"password":       {},
	"secret":         {},
	"dsn":            {},
	"database_url":   {},
	"redis_password": {},
	"api_key":        {},
}

// NewJSONLogger returns a structured log/slog JSON logger that automatically redacts
// JWT bearer tokens, Authorization headers, database DSNs, passwords, and secrets.
func NewJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: RedactAttr,
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// RedactAttr inspects structured log attributes and masks sensitive keys or Bearer values with "[REDACTED]".
func RedactAttr(_ []string, a slog.Attr) slog.Attr {
	keyLower := strings.ToLower(strings.TrimSpace(a.Key))
	if isSensitiveKey(keyLower) {
		return slog.String(a.Key, RedactedValue)
	}
	if a.Value.Kind() == slog.KindString {
		val := a.Value.String()
		if looksSensitiveValue(val) {
			return slog.String(a.Key, RedactedValue)
		}
	}
	return a
}

func isSensitiveKey(keyLower string) bool {
	if _, exact := sensitiveKeys[keyLower]; exact {
		return true
	}
	for k := range sensitiveKeys {
		if strings.Contains(keyLower, k) {
			return true
		}
	}
	return false
}

func looksSensitiveValue(v string) bool {
	lower := strings.ToLower(strings.TrimSpace(v))
	if strings.HasPrefix(lower, "bearer ") {
		return true
	}
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") || strings.HasPrefix(lower, "redis://") {
		return true
	}
	return false
}
