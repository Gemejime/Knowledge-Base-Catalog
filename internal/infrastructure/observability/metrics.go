package observability

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the authoritative Prometheus telemetry collectors for rag-kb-catalog-service.
type Metrics struct {
	Registry                 *prometheus.Registry
	RequestsTotal            *prometheus.CounterVec
	RequestDurationSeconds   *prometheus.HistogramVec
	OptimisticConflictsTotal *prometheus.CounterVec
	MigrationDeltaLag        *prometheus.GaugeVec
	ActiveMigrations         *prometheus.GaugeVec
}

// NewMetrics initializes and registers all service Prometheus metrics on an isolated registry.
func NewMetrics(reg *prometheus.Registry) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}

	m := &Metrics{
		Registry: reg,
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kb_catalog_requests_total",
				Help: "Total gRPC and REST requests partitioned by method, tenant, and status code.",
			},
			[]string{"method", "tenant", "status_code"},
		),
		RequestDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "kb_catalog_request_duration_seconds",
				Help:    "Handler execution latencies in seconds (P50, P90, P99).",
				Buckets: []float64{0.001, 0.0025, 0.005, 0.01, 0.02, 0.05, 0.1, 0.25, 0.5, 1.0},
			},
			[]string{"method", "protocol"},
		),
		OptimisticConflictsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "kb_catalog_optimistic_conflicts_total",
				Help: "Total OCC version mismatch events (HTTP 409 / gRPC Aborted).",
			},
			[]string{"tenant", "operation"},
		),
		MigrationDeltaLag: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "kb_catalog_migration_delta_lag_records",
				Help: "Outstanding delta write backlog before Blue-Green cutover.",
			},
			[]string{"tenant", "kb_id"},
		),
		ActiveMigrations: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "kb_catalog_active_migrations",
				Help: "Currently active Blue-Green migrations in REINDEXING state.",
			},
			[]string{"tenant"},
		),
	}

	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDurationSeconds,
		m.OptimisticConflictsTotal,
		m.MigrationDeltaLag,
		m.ActiveMigrations,
	)

	return m
}

// RecordOptimisticConflict increments kb_catalog_optimistic_conflicts_total for the given tenant and operation.
func (m *Metrics) RecordOptimisticConflict(tenantID, operation string) {
	if m == nil || m.OptimisticConflictsTotal == nil {
		return
	}
	if tenantID == "" {
		tenantID = "unknown"
	}
	m.OptimisticConflictsTotal.WithLabelValues(tenantID, operation).Inc()
}
