package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"

	catalogv1 "github.com/gemejime/rag-kb-catalog-service/api/proto/v1"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/command"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/query"
	"github.com/gemejime/rag-kb-catalog-service/internal/application/workflow"
	"github.com/gemejime/rag-kb-catalog-service/internal/domain/repository"
	rediscache "github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/cache/redis"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/observability"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/persistence/postgres"
	"github.com/gemejime/rag-kb-catalog-service/internal/infrastructure/temporal"
	grpctransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc"
	"github.com/gemejime/rag-kb-catalog-service/internal/transport/grpc/interceptors"
	httptransport "github.com/gemejime/rag-kb-catalog-service/internal/transport/http"
)

func main() {
	logger := observability.NewJSONLogger(os.Stdout, slog.LevelInfo)
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	httpAddr := envOrDefault("HTTP_ADDR", ":8080")
	grpcAddr := envOrDefault("GRPC_ADDR", ":50051")
	dbURL := os.Getenv("DATABASE_URL")
	jwtSecret := os.Getenv("JWT_PUBLIC_KEY")
	if strings.TrimSpace(jwtSecret) == "" {
		logger.Error("startup aborted: JWT_PUBLIC_KEY environment variable is required")
		os.Exit(1)
	}

	metrics := observability.NewMetrics(nil)

	var (
		repo   repository.CatalogRepository
		dbPool *pgxpool.Pool
	)

	if dbURL != "" {
		pool, err := postgres.NewPool(ctx, postgres.PoolConfig{DSN: dbURL})
		if err != nil {
			logger.Error("failed to initialize postgresql pool", slog.String("error", err.Error()))
			os.Exit(1)
		}
		dbPool = pool
		defer dbPool.Close()
		repo = postgres.NewCatalogRepo(dbPool, postgres.NewOutboxRepo(), metrics)
	} else {
		logger.Info("initializing in-memory catalog repository for standalone mode")
		repo = postgres.NewInMemoryCatalogRepo(metrics)
	}

	var (
		cache       repository.VersionedCache
		redisPinger interface{ Ping(context.Context) error }
	)

	redisAddrsEnv := os.Getenv("REDIS_CLUSTER_ADDRS")
	redisPassword := os.Getenv("REDIS_PASSWORD")

	if strings.TrimSpace(redisAddrsEnv) != "" {
		rawAddrs := strings.Split(redisAddrsEnv, ",")
		var cleanedAddrs []string
		for _, addr := range rawAddrs {
			trimmed := strings.TrimSpace(addr)
			if trimmed != "" {
				cleanedAddrs = append(cleanedAddrs, trimmed)
			}
		}

		clusterClient, err := rediscache.NewClusterClient(ctx, rediscache.Config{
			Addrs:    cleanedAddrs,
			Password: redisPassword,
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
	temporalHostPort := os.Getenv("TEMPORAL_HOST_PORT")
	if strings.TrimSpace(temporalHostPort) != "" {
		temporalNamespace := envOrDefault("TEMPORAL_NAMESPACE", "default")
		temporalTaskQueue := envOrDefault("TEMPORAL_TASK_QUEUE", "rag-ingestion-backfill")
		temporalClient, err := temporal.NewClient(temporal.Config{
			HostPort:  temporalHostPort,
			Namespace: temporalNamespace,
			TaskQueue: temporalTaskQueue,
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

	readinessCheck := func(checkCtx context.Context) (bool, bool, error) {
		if dbPool != nil {
			if err := dbPool.Ping(checkCtx); err != nil {
				return false, false, err
			}
		}
		if redisPinger != nil {
			if err := redisPinger.Ping(checkCtx); err != nil {
				return true, false, err
			}
		}
		return true, true, nil
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
		nil,
	)
	router := httptransport.NewRouter(httpHandler, jwtSecret, logger, metrics)

	httpServer := &http.Server{
		Addr:              httpAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptors.UnaryAuthLoggingInterceptor(jwtSecret, logger, metrics)),
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

	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		logger.Error("failed to listen on grpc address", slog.String("addr", grpcAddr), slog.String("error", err.Error()))
		os.Exit(1)
	}

	go func() {
		logger.Info("starting grpc server", slog.String("addr", grpcAddr))
		if serveErr := grpcServer.Serve(grpcLis); serveErr != nil {
			logger.Error("grpc server terminated with error", slog.String("error", serveErr.Error()))
		}
	}()

	go func() {
		logger.Info("starting http server", slog.String("addr", httpAddr))
		if serveErr := httpServer.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			logger.Error("http server terminated with error", slog.String("error", serveErr.Error()))
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, draining connections")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcServer.GracefulStop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("http server graceful shutdown error", slog.String("error", err.Error()))
	}
	logger.Info("rag-kb-catalog-service stopped cleanly")
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
