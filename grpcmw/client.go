package grpcmw

import (
	"context"

	"github.com/oberlevd/platform-lib/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// RequestIDUnaryClientInterceptor прокидывает request_id в исходящие
// gRPC-вызовы через metadata "x-request-id".
//
// Если в контексте уже есть request_id (например, пришёл с входящего
// запроса через серверный RequestIDUnaryInterceptor) - используется он.
// Если нет - генерируется новый и кладётся и в context, и в metadata,
// чтобы вся исходящая цепочка и локальные логи видели один id.
//
// Уже выставленный в outgoing metadata x-request-id не перезаписывается
// (caller мог задать его явно).
func RequestIDUnaryClientInterceptor() grpc.UnaryClientInterceptor {
	return func(
		ctx context.Context,
		method string,
		req any,
		reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		opts ...grpc.CallOption,
	) error {
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
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// ClientChain - набор client interceptor'ов по умолчанию.
// Подключается при dial:
//
//	conn, err := grpc.Dial(addr, grpcmw.ClientChain(), ...)
func ClientChain() grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(
		RequestIDUnaryClientInterceptor(),
	)
}

// ClientStreamChain - stream client interceptor'ы (request_id).
func ClientStreamChain() grpc.DialOption {
	return grpc.WithChainStreamInterceptor(
		RequestIDStreamClientInterceptor(),
	)
}

// ClientOptions - unary + stream dial options.
func ClientOptions() []grpc.DialOption {
	return []grpc.DialOption{
		ClientChain(),
		ClientStreamChain(),
	}
}
