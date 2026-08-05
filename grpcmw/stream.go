package grpcmw

import (
	"context"
	"time"

	"github.com/oberlevd/platform-lib/logger"
	platformmetrics "github.com/oberlevd/platform-lib/metrics"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// wrappedServerStream подменяет Context() у grpc.ServerStream, чтобы
// request_id и обогащённый логгер, положенные stream-interceptor'ами,
// были видны хендлеру через stream.Context() / logger.FromContext.
type wrappedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (w *wrappedServerStream) Context() context.Context {
	return w.ctx
}

// RequestIDStreamInterceptor генерирует или принимает request_id
// (metadata "x-request-id") и кладёт его в context stream'а.
// Должен быть ПЕРВЫМ в цепочке stream-interceptor'ов - по той же
// причине, что и unary-аналог.
func RequestIDStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()
		id := requestIDFromIncomingMetadata(ctx)
		if id == "" {
			id = logger.NewRequestID()
		}
		ctx = logger.WithRequestID(ctx, id)
		return handler(srv, &wrappedServerStream{ServerStream: ss, ctx: ctx})
	}
}

// LoggingStreamInterceptor логирует открытие/завершение stream RPC:
// метод, статус, длительность, request_id. Кладёт в context обогащённый
// логгер - хендлер достаёт его через logger.FromContext(stream.Context()).
func LoggingStreamInterceptor(base *logger.Logger) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		ctx := ss.Context()
		start := time.Now()
		reqID := logger.RequestIDFromContext(ctx)
		l := base.With(
			"request_id", reqID,
			"grpc_method", info.FullMethod,
			"grpc_stream", true,
		)
		ctx = logger.WithContext(ctx, l)
		wrapped := &wrappedServerStream{ServerStream: ss, ctx: ctx}

		err := handler(srv, wrapped)
		duration := time.Since(start)
		code := status.Code(err)
		if err != nil {
			l.Error(ctx, "grpc stream failed", err,
				"grpc_code", code.String(),
				"duration_ms", duration.Milliseconds(),
			)
		} else {
			l.Info(ctx, "grpc stream completed",
				"grpc_code", code.String(),
				"duration_ms", duration.Milliseconds(),
			)
		}
		return err
	}
}

// MetricsStreamInterceptor пишет RED-метрики на каждый stream RPC.
// Порядок: СНАРУЖИ Recovery (раньше в Chain), иначе panic обойдёт
// инкремент RequestsTotal - та же логика, что у unary Metrics.
func MetricsStreamInterceptor(m *platformmetrics.RED) grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		m.RequestsInFlight.WithLabelValues(info.FullMethod).Inc()
		defer m.RequestsInFlight.WithLabelValues(info.FullMethod).Dec()
		start := time.Now()
		err := handler(srv, ss)
		duration := time.Since(start)
		code := status.Code(err)
		m.RequestsTotal.WithLabelValues(info.FullMethod, code.String()).Inc()
		m.RequestDuration.WithLabelValues(info.FullMethod, code.String()).Observe(duration.Seconds())
		return err
	}
}

// RecoveryStreamInterceptor ловит panic в stream-хендлере, логирует
// и возвращает codes.Internal. Должен быть ближе всех к хендлеру
// (после logging/metrics в Chain).
func RecoveryStreamInterceptor() grpc.StreamServerInterceptor {
	return func(
		srv any,
		ss grpc.ServerStream,
		info *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) (err error) {
		defer func() {
			if r := recover(); r != nil {
				ctx := ss.Context()
				l := logger.FromContext(ctx)
				l.Error(ctx, "grpc stream handler panicked", nil,
					"panic", r,
					"grpc_method", info.FullMethod,
				)
				err = status.Errorf(codes.Internal, "internal error")
			}
		}()
		return handler(srv, ss)
	}
}

// RequestIDStreamClientInterceptor прокидывает request_id в исходящие
// client stream вызовы через metadata "x-request-id" - зеркало
// unary RequestIDUnaryClientInterceptor.
func RequestIDStreamClientInterceptor() grpc.StreamClientInterceptor {
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		opts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		id := logger.RequestIDFromContext(ctx)
		if id == "" {
			id = logger.NewRequestID()
			ctx = logger.WithRequestID(ctx, id)
		}
		md, ok := metadata.FromOutgoingContext(ctx)
		if !ok {
			md = metadata.MD{}
		} else {
			md = md.Copy()
		}
		if len(md.Get(requestIDMetadataKey)) == 0 {
			md.Set(requestIDMetadataKey, id)
		}
		ctx = metadata.NewOutgoingContext(ctx, md)
		return streamer(ctx, desc, cc, method, opts...)
	}
}
