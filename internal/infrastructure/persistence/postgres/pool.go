package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolConfig defines tuning parameters for the PgBouncer-compatible PostgreSQL connection pool.
type PoolConfig struct {
	DSN             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// ParsePoolConfig parses the PostgreSQL DSN and enforces default_query_exec_mode = exec
// (pgx.QueryExecModeExec) to eliminate prepared statement collisions under PgBouncer transaction pooling (ADR-004).
func ParsePoolConfig(cfg PoolConfig) (*pgxpool.Config, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("database DSN is required")
	}
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("failed to parse postgres pool config: %w", err)
	}

	// Non-negotiable invariant (ADR-004): Enforce pgx.QueryExecModeExec (default_query_exec_mode=exec)
	// for PgBouncer transaction pooling compatibility.
	poolCfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec

	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	} else {
		poolCfg.MaxConns = 25
	}
	if cfg.MinConns >= 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	} else {
		poolCfg.MaxConnLifetime = 30 * time.Minute
	}
	if cfg.MaxConnIdleTime > 0 {
		poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	} else {
		poolCfg.MaxConnIdleTime = 5 * time.Minute
	}

	return poolCfg, nil
}

// NewPool creates and verifies a PgBouncer-compatible pgxpool.Pool with QueryExecModeExec.
func NewPool(ctx context.Context, cfg PoolConfig) (*pgxpool.Pool, error) {
	poolCfg, err := ParsePoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize pgxpool: %w", err)
	}
	return pool, nil
}
