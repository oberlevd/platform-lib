package lifecycle

import "context"

// gracefulStopper — минимальный интерфейс, которому удовлетворяет
// *grpc.Server. Выделен отдельно (вместо принятия конкретного типа),
// чтобы GRPCServerShutdown можно было протестировать без поднятия
// настоящего gRPC-сервера — см. lifecycle/grpc_test.go.
type gracefulStopper interface {
	GracefulStop()
	Stop()
}

// GRPCServerShutdown возвращает ShutdownFunc для *grpc.Server: пытается
// GracefulStop (дожидается завершения in-flight запросов и сам
// перестаёт принимать новые), но GracefulStop ничего не знает про
// context — это блокирующий вызов без таймаута. Если он не укладывается
// в дедлайн переданного в ShutdownFunc контекста, GRPCServerShutdown
// принудительно вызывает Stop(), чтобы не превысить общий бюджет
// времени на shutdown сервиса (см. lifecycle.Manager.Run).
//
// Пример:
//
//	lc.Register("grpc-server", lifecycle.GRPCServerShutdown(grpcServer))
func GRPCServerShutdown(srv gracefulStopper) ShutdownFunc {
	return func(ctx context.Context) error {
		done := make(chan struct{})
		go func() {
			srv.GracefulStop()
			close(done)
		}()

		select {
		case <-done:
			return nil
		case <-ctx.Done():
			srv.Stop()
			return ctx.Err()
		}
	}
}
