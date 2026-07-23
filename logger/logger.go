// Package logger предоставляет единый структурированный JSON-логгер
// для всех сервисов платформы. Обёртка над стандартным log/slog:
// не тянем внешние зависимости, получаем JSON "из коробки", быстрый
// и достаточный набор фич для платформенных нужд.
//
// Дизайн-принципы:
//   - Всегда JSON в stdout. Никаких файлов, никаких прямых сетевых
//     appender'ов в ELK — доставку логов делает агент на ноде
//     (Vector), а не сам процесс.
//   - Обязательные поля (service, version, env) прибиваются один раз
//     при инициализации и присутствуют в каждой строке лога.
//   - request_id прокидывается через context.Context и должен
//     логироваться на каждый вызов, если он есть в контексте.
//   - Чувствительные поля маскируются до сериализации, не полагаемся
//     на дисциплину разработчика в каждом сервисе.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
)

// Config — параметры инициализации логгера сервиса.
type Config struct {
	// Service — имя сервиса, попадает в каждую строку лога.
	Service string
	// Version — версия/git-sha сборки.
	Version string
	// Env — окружение: dev/staging/prod.
	Env string
	// Level — минимальный уровень логирования. По умолчанию Info.
	Level slog.Level
	// Output — куда писать логи. По умолчанию os.Stdout.
	// Параметр существует в основном для тестов.
	Output io.Writer
	// RedactKeys — дополнительные имена полей, которые нужно маскировать,
	// помимо базового набора (см. redact.go).
	RedactKeys []string
}

// Logger — обёртка над *slog.Logger с платформенными хелперами.
type Logger struct {
	base *slog.Logger
}

// New создаёт платформенный логгер согласно конфигу.
// Вызывается один раз в main() каждого сервиса.
func New(cfg Config) *Logger {
	out := cfg.Output
	if out == nil {
		out = os.Stdout
	}

	redactor := newRedactor(cfg.RedactKeys)

	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level: cfg.Level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// Приводим ключ времени к единому имени "timestamp",
			// чтобы во всех сервисах поле называлось одинаково —
			// это важно для index mapping в Elasticsearch.
			if a.Key == slog.TimeKey && len(groups) == 0 {
				a.Key = "timestamp"
			}
			if a.Key == slog.MessageKey && len(groups) == 0 {
				a.Key = "message"
			}
			return redactor.replaceAttr(groups, a)
		},
	})

	base := slog.New(handler).With(
		slog.String("service", cfg.Service),
		slog.String("version", cfg.Version),
		slog.String("env", cfg.Env),
	)

	return &Logger{base: base}
}

// ctxKey — приватный тип для ключей контекста, чтобы избежать коллизий
// с ключами из других пакетов.
type ctxKey struct{ name string }

var loggerCtxKey = ctxKey{"platform-logger"}

// WithContext кладёт логгер (уже обогащённый request_id и другими полями
// через With) в context, чтобы прокидывать его дальше по вызовам без
// явной передачи параметром.
func WithContext(ctx context.Context, l *Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey, l)
}

// FromContext достаёт логгер из контекста. Если логгера в контексте нет
// (например, забыли прокинуть, или это фоновая задача без входящего
// запроса), возвращает logger.Default() — чтобы код никогда не падал
// из-за отсутствия логгера, но проблема была видна по логам без service/env.
func FromContext(ctx context.Context) *Logger {
	if l, ok := ctx.Value(loggerCtxKey).(*Logger); ok {
		return l
	}
	return defaultLogger
}

// With возвращает новый Logger с добавленными полями. Используется для
// обогащения контекстными данными: request_id, user_id, домен-специфичные
// поля и т.д.
func (l *Logger) With(args ...any) *Logger {
	return &Logger{base: l.base.With(args...)}
}

func (l *Logger) Debug(ctx context.Context, msg string, args ...any) {
	l.base.DebugContext(ctx, msg, args...)
}

func (l *Logger) Info(ctx context.Context, msg string, args ...any) {
	l.base.InfoContext(ctx, msg, args...)
}

func (l *Logger) Warn(ctx context.Context, msg string, args ...any) {
	l.base.WarnContext(ctx, msg, args...)
}

// Error логирует ошибку. err передаётся отдельным аргументом и кладётся
// в поле "error" как строка — если err == nil, поле не добавляется.
func (l *Logger) Error(ctx context.Context, msg string, err error, args ...any) {
	if err != nil {
		args = append(args, slog.String("error", err.Error()))
	}
	l.base.ErrorContext(ctx, msg, args...)
}

// defaultLogger — фолбэк для случаев, когда контекст без логгера.
// Уровень Info, вывод в stdout, но без service/version/env — так
// в Kibana сразу видно "потерянный" контекст логирования.
var defaultLogger = New(Config{
	Service: "unknown",
	Version: "unknown",
	Env:     "unknown",
	Level:   slog.LevelInfo,
})
