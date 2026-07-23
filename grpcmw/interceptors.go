// Package grpcmw содержит стандартные gRPC unary interceptor'ы платформы:
// request_id, логирование каждого вызова, RED-метрики и recovery от паники.
// Порядок подключения важен — см. пример в конце файла.
package grpcmw

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/oberlevd/platform-lib/logger"
	platformmetrics "github.com/oberlevd/platform-lib/metrics"
)

// RequestIDUnaryInterceptor генерирует request_id (если он не пришёл
// от вызывающего сервиса через metadata "x-request-id") и кладёт его
// в context. Должен быть ПЕРВЫМ в цепочке интерцепторов, чтобы все
// остальные (логирование, метрики) могли на него опираться.
func RequestIDUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		id := requestIDFromIncomingMetadata(ctx)
		if id == "" {
			id = logger.NewRequestID()
		}
		ctx = logger.WithRequestID(ctx, id)
		return handler(ctx, req)
	}
}

// LoggingUnaryInterceptor логирует каждый вызов: метод, статус, длительность,
// request_id. Кладёт в контекст логгер, обогащённый этими же полями —
// хендлеры сервиса получают через logger.FromContext(ctx) логгер, где
// request_id и method уже проставлены, ничего прокидывать вручную не надо.
func LoggingUnaryInterceptor(base *logger.Logger) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		start := time.Now()
		reqID := logger.RequestIDFromContext(ctx)

		l := base.With(
			"request_id", reqID,
			"grpc_method", info.FullMethod,
		)
		ctx = logger.WithContext(ctx, l)

		resp, err := handler(ctx, req)

		duration := time.Since(start)
		code := status.Code(err)

		if err != nil {
			l.Error(ctx, "grpc request failed", err,
				"grpc_code", code.String(),
				"duration_ms", duration.Milliseconds(),
			)
		} else {
			l.Info(ctx, "grpc request completed",
				"grpc_code", code.String(),
				"duration_ms", duration.Milliseconds(),
			)
		}

		return resp, err
	}
}

// MetricsUnaryInterceptor пишет RED-метрики на каждый вызов.
//
// ВАЖНО про порядок: этот интерцептор должен идти СНАРУЖИ (раньше)
// RecoveryUnaryInterceptor в цепочке (см. Chain ниже). Если поставить
// его после Recovery (то есть ближе к хендлеру), паника из хендлера
// развернёт стек мимо строк после handler(ctx, req) — счётчики
// RequestsTotal/RequestDuration для паникующих вызовов просто не
// выполнятся, останется только Dec() из defer. Именно в таком (неверном)
// порядке пакет был написан изначально — баг, поправлено здесь.
func MetricsUnaryInterceptor(m *platformmetrics.RED) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		m.RequestsInFlight.WithLabelValues(info.FullMethod).Inc()
		defer m.RequestsInFlight.WithLabelValues(info.FullMethod).Dec()

		start := time.Now()
		resp, err := handler(ctx, req)
		duration := time.Since(start)

		code := status.Code(err)
		m.RequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		m.RequestDuration.WithLabelValues(info.FullMethod, code.String()).Observe(duration.Seconds())

		return resp, err
	}
}

// RecoveryUnaryInterceptor перехватывает панику в хендлере, логирует её
// и возвращает клиенту codes.Internal вместо падения всего процесса.
// Должен идти ПОСЛЕ logging- и metrics-интерцепторов в цепочке (см.
// Chain ниже) — то есть быть ближе всех к реальному хендлеру. Тогда
// паника гасится здесь и наружу (в Metrics) уходит уже обычная пара
// (resp, err), а не паника — и метрики по паникующим вызовам считаются
// корректно.
func RecoveryUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				l := logger.FromContext(ctx)
				l.Error(ctx, "grpc handler panicked", nil,
					"panic", r,
					"grpc_method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// Chain — порядок подключения по умолчанию. Собирает все стандартные
// interceptor'ы в правильном порядке:
//  1. RequestID   — генерирует/принимает request_id первым делом.
//  2. Logging     — кладёт в контекст обогащённый логгер.
//  3. Metrics     — считает RED-метрики, включая вызовы, упавшие с
//     паникой (важно для корректности Error Rate) — это работает,
//     только если Metrics стоит СНАРУЖИ Recovery, как здесь.
//  4. Recovery    — ловит панику ближе всех к хендлеру, используя уже
//     готовый логгер; превращает панику в обычный error до того, как
//     он дойдёт до Metrics.
func Chain(base *logger.Logger, m *platformmetrics.RED) grpc.ServerOption {
	return grpc.ChainUnaryInterceptor(
		RequestIDUnaryInterceptor(),
		LoggingUnaryInterceptor(base),
		MetricsUnaryInterceptor(m),
		RecoveryUnaryInterceptor(),
	)
}

// requestIDMetadataKey — имя заголовка, через который request_id
// прокидывается между сервисами. Если gateway или BFF на фронте уже
// сгенерировал id выше по цепочке, читаем его здесь вместо генерации
// нового — тогда один и тот же id виден сквозь всю цепочку вызовов
// в Kibana.
const requestIDMetadataKey = "x-request-id"

func requestIDFromIncomingMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(requestIDMetadataKey)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
