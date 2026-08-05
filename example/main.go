// Пример минимального gRPC+HTTP сервиса на platform-lib:
// config → logger → mssql → healthcheck → metrics → grpcmw → lifecycle.
//
// Запуск локально (без реальной MSSQL упадёт на Open - для демо можно
// временно закомментировать блок mssql или поднять SQL Server через
// docker compose в этой же директории).
package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/oberlevd/platform-lib/config"
	"github.com/oberlevd/platform-lib/grpcmw"
	"github.com/oberlevd/platform-lib/healthcheck"
	"github.com/oberlevd/platform-lib/lifecycle"
	"github.com/oberlevd/platform-lib/logger"
	"github.com/oberlevd/platform-lib/metrics"
	"github.com/oberlevd/platform-lib/mssql"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health/grpc_health_v1"
)

// Config - конфиг сервиса. Вложенный mssql.Config без собственного env-тега
// обрабатывается config.Load рекурсивно (его поля читают MSSQL_* из ENV).
type Config struct {
	HTTPAddr string `env:"HTTP_ADDR" default:":8080"`
	GRPCAddr string `env:"GRPC_ADDR" default:":9090"`
	GitSHA   string `env:"GIT_SHA" default:"unknown"`
	Env      string `env:"ENV" default:"dev"`
	MSSQL    mssql.Config
}

func main() {
	var cfg Config
	if err := config.Load(&cfg); err != nil {
		// Логгера ещё нет - пишем в stderr и выходим.
		_, _ = os.Stderr.WriteString("config load failed: " + err.Error() + "\n")
		os.Exit(1)
	}

	log := logger.New(logger.Config{
		Service: "example-service",
		Version: cfg.GitSHA,
		Env:     cfg.Env,
	})
	ctx := context.Background()
	log.Info(ctx, "config loaded", "config", config.Redacted(&cfg))

	db, err := mssql.Open(ctx, cfg.MSSQL)
	if err != nil {
		log.Error(ctx, "mssql open failed", err)
		os.Exit(1)
	}

	hc := healthcheck.New()
	hc.Register("mssql", mssql.Checker(db))

	red := metrics.NewRED(prometheus.DefaultRegisterer, "example_service")

	// ServerOptions = unary Chain + stream ChainStream
	// (request_id, logging, metrics, recovery для обоих типов RPC).
	grpcServer := grpc.NewServer(grpcmw.ServerOptions(log, red)...)
	// Здесь: pb.RegisterYourServiceServer(grpcServer, impl)
	grpc_health_v1.RegisterHealthServer(grpcServer, healthcheck.NewGRPCServer(hc))

	mux := http.NewServeMux()
	hc.RegisterHTTP(mux)
	mux.Handle("/metrics", promhttp.Handler())
	httpSrv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	lis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Error(ctx, "grpc listen failed", err, "addr", cfg.GRPCAddr)
		os.Exit(1)
	}

	go func() {
		log.Info(ctx, "grpc server listening", "addr", cfg.GRPCAddr)
		if err := grpcServer.Serve(lis); err != nil {
			log.Error(ctx, "grpc serve failed", err)
		}
	}()

	go func() {
		log.Info(ctx, "http server listening", "addr", cfg.HTTPAddr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(ctx, "http serve failed", err)
		}
	}()

	// Порядок Register = обратный порядок остановки (как defer):
	// сначала зависимости (mssql), потом серверы - при shutdown
	// сначала grpc/http перестанут принимать трафик, потом закроется пул БД.
	lc := lifecycle.New(log)
	lc.Register("mssql", lifecycle.CloserShutdown(db))
	lc.Register("http", lifecycle.HTTPServerShutdown(httpSrv))
	lc.Register("grpc", lifecycle.GRPCServerShutdown(grpcServer))

	log.Info(ctx, "service started")
	lc.Run(ctx, 25*time.Second)
	log.Info(ctx, "service stopped")
}
