package healthcheck

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// GRPCServer реализует grpc_health_v1.HealthServer поверх тех же
// readiness-проверок, что и HTTP /readyz - так поведение liveness/
// readiness одинаковое независимо от того, кто спрашивает: k8s через
// HTTP или Envoy/service mesh через gRPC health protocol.
type GRPCServer struct {
	grpc_health_v1.UnimplementedHealthServer
	h *Handler
}

// NewGRPCServer оборачивает существующий Handler (с уже
// зарегистрированными Checker'ами) в grpc_health_v1.HealthServer.
func NewGRPCServer(h *Handler) *GRPCServer {
	return &GRPCServer{h: h}
}

// Check - единоразовая проверка статуса. service игнорируется: этот
// сервер сообщает статус всего процесса целиком, а не по отдельным
// gRPC-сервисам внутри него (для платформенного masштаба этого
// достаточно; per-service granularity можно добавить отдельно, если
// понадобится).
func (s *GRPCServer) Check(ctx context.Context, req *grpc_health_v1.HealthCheckRequest) (*grpc_health_v1.HealthCheckResponse, error) {
	checkers, timeout := s.h.snapshot()

	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	_, allOK := runChecks(checkCtx, checkers)

	if !allOK {
		return &grpc_health_v1.HealthCheckResponse{
			Status: grpc_health_v1.HealthCheckResponse_NOT_SERVING,
		}, nil
	}

	return &grpc_health_v1.HealthCheckResponse{
		Status: grpc_health_v1.HealthCheckResponse_SERVING,
	}, nil
}

// Watch - потоковая версия. Платформа сознательно не поддерживает
// streaming health-check на старте (усложняет реализацию, а polling
// через Check раз в несколько секунд балансировщиками более чем
// достаточен для наших SLA) - явно возвращаем Unimplemented, а не
// молча зависаем, чтобы клиент сразу понял, что нужно использовать Check.
func (s *GRPCServer) Watch(req *grpc_health_v1.HealthCheckRequest, stream grpc_health_v1.Health_WatchServer) error {
  return status.Error(codes.Unimplemented, "Watch is not supported, use Check")
}
