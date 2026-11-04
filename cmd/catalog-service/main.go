package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	"github.com/gemejime/rag-kb-catalog-service/api/openapi"
	catalogv1 "github.com/gemejime/rag-kb-catalog-service/api/proto/v1"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/config"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/temporal"
	grpctransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc/interceptors"
	httptransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/http"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		logger := observability.NewJSONLogger(os.Stdout, slog.LevelError)
		logger.Error("configuration loading failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	logger := observability.NewJSONLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	logger.Info("starting rag-kb-catalog-service",
		slog.String("environment", cfg.Environment),
		slog.String("log_level", cfg.LogLevel.String()),
		slog.String("http_addr", cfg.HTTPAddr),
		slog.String("grpc_addr", cfg.GRPCAddr),
		slog.String("metrics_addr", cfg.MetricsAddr),
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	metrics := observability.NewMetrics(nil)

	var (
		repo   repository.CatalogRepository
		dbPool *pgxpool.Pool
	)

	if cfg.Postgres.DSN != "" {
		pool, err := postgres.NewPool(ctx, cfg.Postgres.PoolConfig())
		if err != nil {
			logger.Error("failed to initialize postgresql pool", slog.String("error", err.Error()))
			os.Exit(1)
		}
		dbPool = pool
		defer dbPool.Close()
		metrics.RegisterPgxPoolStats(
			func() int32 { return dbPool.Stat().AcquiredConns() },
			func() int32 { return dbPool.Stat().MaxConns() },
		)
		repo = postgres.NewCatalogRepo(dbPool, postgres.NewOutboxRepo(), metrics)
	} else {
		logger.Info("initializing in-memory catalog repository for standalone mode")
		repo = postgres.NewInMemoryCatalogRepo(metrics)
	}

	var (
		cache       repository.VersionedCache
		redisPinger interface{ Ping(context.Context) error }
	)

	if len(cfg.Redis.Addrs) > 0 {
		clusterClient, err := rediscache.NewClusterClient(ctx, rediscache.Config{
			Addrs:    cfg.Redis.Addrs,
			Password: cfg.Redis.Password,
		}, logger)
		if err != nil {
			logger.Error("failed to initialize redis cluster client", slog.String("error", err.Error()))
			os.Exit(1)
		}
		defer clusterClient.Close()
		cache = clusterClient
		redisPinger = clusterClient
	} else {
		logger.Info("initializing in-memory versioned cache for standalone mode")
		memCache := rediscache.NewInMemoryVersionedCache()
		cache = memCache
		redisPinger = memCache
	}

	var trigger workflow.IngestionTrigger
	if cfg.Temporal.HostPort != "" {
		temporalClient, err := temporal.NewClient(temporal.Config{
			HostPort:  cfg.Temporal.HostPort,
			Namespace: cfg.Temporal.Namespace,
			TaskQueue: cfg.Temporal.TaskQueue,
		}, logger)
		if err != nil {
			logger.Error("failed to initialize temporal client, falling back to no-op trigger", slog.String("error", err.Error()))
			trigger = workflow.NewNoopIngestionTrigger()
		} else {
			defer temporalClient.Close()
			trigger = temporalClient
		}
	} else {
		logger.Info("TEMPORAL_HOST_PORT not configured; using fail-closed noop ingestion trigger")
		trigger = workflow.NewNoopIngestionTrigger()
	}

	createCmd := command.NewCreateKnowledgeBaseCommand(repo, cache)
	updateCmd := command.NewUpdateKnowledgeBaseCommand(repo, cache)
	triggerCmd := command.NewTriggerReindexingCommand(repo, cache, trigger)
	promoteCmd := command.NewPromotePointerCommand(repo, cache)
	abortCmd := command.NewAbortReindexingCommand(repo, cache)
	deleteCmd := command.NewDeleteKnowledgeBaseCommand(repo, cache)
	getQuery := query.NewGetKnowledgeBaseQuery(repo, cache)
	listQuery := query.NewListKnowledgeBasesQuery(repo)

	var readinessCheck httptransport.ReadinessCheckFunc
	if dbPool != nil || len(cfg.Redis.Addrs) > 0 {
		readinessCheck = func(checkCtx context.Context) (bool, bool, error) {
			if dbPool != nil {
				if err := dbPool.Ping(checkCtx); err != nil {
					return false, false, err
				}
			}
			if len(cfg.Redis.Addrs) > 0 && redisPinger != nil {
				if err := redisPinger.Ping(checkCtx); err != nil {
					return true, false, err
				}
			}
			return true, true, nil
		}
	}

	httpHandler := httptransport.NewHandler(
		createCmd,
		updateCmd,
		triggerCmd,
		promoteCmd,
		abortCmd,
		deleteCmd,
		getQuery,
		listQuery,
		readinessCheck,
		openapi.SpecJSON(),
	)
	router := httptransport.NewRouter(httpHandler, cfg.JWTSecret, logger, metrics)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	metricsServer := &http.Server{
		Addr:              cfg.MetricsAddr,
		Handler:           httptransport.NewMetricsRouter(metrics),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.UnaryAuthLoggingInterceptor(cfg.JWTSecret, logger, metrics)),
	)
	catalogServer := grpctransport.NewCatalogServer(
		createCmd,
		updateCmd,
		triggerCmd,
		promoteCmd,
		abortCmd,
		deleteCmd,
		getQuery,
		listQuery,
	)
	catalogv1.RegisterCatalogServiceServer(grpcServer, catalogServer)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		logger.Error("failed to listen on grpc address", slog.String("addr", cfg.GRPCAddr), slog.String("error", err.Error()))
		os.Exit(1)
	}

	errCh := make(chan error, 3)

	go func() {
		logger.Info("starting grpc server", slog.String("addr", cfg.GRPCAddr))
		if serveErr := grpcServer.Serve(grpcLis); serveErr != nil {
			logger.Error("grpc server terminated with error", slog.String("error", serveErr.Error()))
			errCh <- serveErr
		}
	}()

	go func() {
		logger.Info("starting http server", slog.String("addr", cfg.HTTPAddr))
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("http server terminated with error", slog.String("error", serveErr.Error()))
			errCh <- serveErr
		}
	}()

	go func() {
		logger.Info("starting internal metrics server", slog.String("addr", cfg.MetricsAddr))
		if serveErr := metricsServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("metrics server terminated with error", slog.String("error", serveErr.Error()))
			errCh <- serveErr
		}
	}()

	var exitCode int
	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
	case err := <-errCh:
		logger.Error("server encountered unexpected error, initiating shutdown", slog.String("error", err.Error()))
		exitCode = 1
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcServer.GracefulStop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server graceful shutdown error", slog.String("error", err.Error()))
	}
	if err := metricsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("metrics server graceful shutdown error", slog.String("error", err.Error()))
	}
	logger.Info("rag-kb-catalog-service stopped cleanly")
	if exitCode != 0 {
		os.Exit(exitCode)
	}
}
