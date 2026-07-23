package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

type requestIDKey struct{}

var reqIDCtxKey = requestIDKey{}

// NewRequestID генерирует новый request_id: 16 случайных байт в hex
// (32 символа). Не претендует на роль distributed trace id — это
// внутриплатформенный корреляционный идентификатор одного запроса
// от входа до всех подзапросов, которые он породил.
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Практически недостижимо (crypto/rand не должен фейлиться),
		// но не должно ронять сервис — отдаём константу-маркер,
		// по которой в логах сразу видно аномалию.
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// WithRequestID кладёт request_id в контекст.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, reqIDCtxKey, id)
}

// RequestIDFromContext достаёт request_id из контекста. Возвращает
// пустую строку, если его там нет.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(reqIDCtxKey).(string); ok {
		return id
	}
	return ""
}
