package lifecycle

import (
	"context"
	"net/http"
)

// HTTPServerShutdown возвращает ShutdownFunc для *http.Server (например,
// сервера под /metrics или /healthz, /readyz). net/http уже умеет
// принимать context.Context в Shutdown, поэтому здесь просто тонкая
// адаптация под ShutdownFunc — без дополнительной логики форс-стопа,
// как в GRPCServerShutdown: http.Server.Shutdown сам возвращает ошибку,
// если дедлайн контекста истёк, не оставляя соединения висеть.
//
// Пример:
//
//	lc.Register("metrics-http-server", lifecycle.HTTPServerShutdown(metricsSrv))
func HTTPServerShutdown(srv *http.Server) ShutdownFunc {
	return func(ctx context.Context) error {
		return srv.Shutdown(ctx)
	}
}
