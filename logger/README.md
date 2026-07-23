# logger

Пакет `logger` предоставляет единый структурированный JSON-логгер для всех сервисов платформы. Это обёртка над стандартным `log/slog` (Go 1.21+), которая гарантирует единообразие формата, обязательные поля, маскировку чувствительных данных и бесшовную интеграцию с контекстом.

---

## Особенности

- **Всегда JSON в stdout** – логи пишутся в стандартный вывод, доставку в ELK/OpenSearch выполняет агент на ноде (например, Vector).
- **Обязательные поля** – `service`, `version`, `env` прибиваются при инициализации и присутствуют в каждой записи.
- **Прокидывание через контекст** – логгер обогащается `request_id` и другими полями, передаётся через `context.Context`.
- **Автоматическая маскировка секретов** – чувствительные поля (`password`, `token`, `api_key` и т.д.) заменяются на `***REDACTED***` до сериализации. Дополнительные ключи можно добавить.
- **Защита от случайного логирования DSN/строк подключения** – даже если секрет спрятан внутри строкового значения (например, `"dsn=Server=...;Password=..."`), он будет вырезан.
- **Нулевая зависимость** – только стандартная библиотека.

---

## Установка

```bash
go get github.com/oberlevd/platform-lib/logger
```

## Конфигурация

Структура `Config`:

```go
type Config struct {
    Service    string        // имя сервиса
    Version    string        // версия или git-sha
    Env        string        // окружение: dev/staging/prod
    Level      slog.Level    // минимальный уровень (по умолчанию Info)
    Output     io.Writer     // куда писать (по умолчанию os.Stdout)
    RedactKeys []string      // дополнительные имена полей для маскировки
}
```

## Использование

### Создание логгера

```go
import "github.com/oberlevd/platform-lib/logger"

func main() {
    log := logger.New(logger.Config{
        Service: "order-service",
        Version: "v1.2.3-abc123",
        Env:     "prod",
        Level:   slog.LevelInfo,
        // RedactKeys: []string{"custom_secret"},
    })

    ctx := context.Background()
    log.Info(ctx, "service started")
}
```

### Логирование с контекстом и полями

```go
ctx := logger.WithRequestID(context.Background(), logger.NewRequestID())
log.Info(ctx, "processing order", "order_id", 12345)
```

### Обогащение логгера дополнительными полями (цепочка)

```go
logWithUser := log.With("user_id", 42)
logWithUser.Info(ctx, "user action", "action", "login")
```

### Прокидывание логгера через контекст

```go
ctx = logger.WithContext(ctx, log)
// ... в другом месте
log := logger.FromContext(ctx)
log.Info(ctx, "inside handler")
```

> Если в контексте нет логгера, FromContext возвращает "дефолтный" логгер с полями unknown – это сразу заметно в Kibana.

### Логирование ошибок

```go
err := someFunc()
log.Error(ctx, "failed to process", err, "retry_count", 3)
// поле "error" будет добавлено автоматически
```

### Маскировка секретов

Пакет автоматически маскирует поля, имена которых содержат:

- `password`, `passwd`
- `secret`
- `token`, `authorization`, `api_key`, `apikey`
- `private_key`
- `connection_string`, `conn_str`

и другие (регистронезависимо, по подстроке)

Дополнительные имена можно передать через `RedactKeys`.

Кроме того, маскируются значения внутри строк, если они похожи на `key=value` и ключ чувствительный. Например:

```go
log.Info(ctx, "dsn", "Server=host;Password=abc123;User=sa")
// в логе: "dsn": "Server=host;Password=***REDACTED***;User=sa"
```

Это защита от случайного логирования полных DSN или SQL-строк с параметрами.

## API

### `New(cfg Config) *Logger`

Создаёт логгер с заданной конфигурацией.

### `WithContext(ctx context.Context, l *Logger) context.Context`

Кладёт логгер в контекст.

### `FromContext(ctx context.Context) *Logger`

Достаёт логгер из контекста. Если его нет – возвращает `defaultLogger`.

### `(l *Logger) With(args ...any) *Logger`

Возвращает новый логгер с добавленными полями (аналогично `slog.With`).

## Методы логирования

- `Debug(ctx context.Context, msg string, args ...any)`
- `Info(ctx context.Context, msg string, args ...any)`
- `Warn(ctx context.Context, msg string, args ...any)`
- `Error(ctx context.Context, msg string, err error, args ...any)`

Все методы принимают контекст и поддерживают пары `key, value` для дополнительных полей.

### `NewRequestID() string`

Генерирует 32-символьный hex-идентификатор запроса.

### `WithRequestID(ctx context.Context, id string) context.Context`

Кладёт `request_id` в контекст.

### `RequestIDFromContext(ctx context.Context) string`

Извлекает `request_id` из контекста.

## Пример полного лога

```JSON
{
  "timestamp": "2026-07-23T23:10:24.000Z",
  "level": "INFO",
  "service": "payment-service",
  "version": "v2.0.1-5f3a7b2",
  "env": "prod",
  "message": "payment processed",
  "payment_id": 9876,
  "amount": 150.75,
  "request_id": "a1b2c3d4e5f67890a1b2c3d4e5f67890"
}
```

## Примечания

- Логгер не пишет в файлы и **не отправляет логи напрямую в ELK** – это задача инфраструктурного агента (Vector).
- Для тестов можно подменить `Output` на `bytes.Buffer`.
- Уровень логирования можно менять динамически через `slog.SetLogLoggerLevel`, но рекомендуется задавать его при инициализации.
- Маскировка работает как для отдельных полей, так и для вложенных значений в `slog.Group`.

## Тестирование

В пакете есть юнит-тесты, которые можно запустить стандартной командой:

```bash
go test -v ./logger
```
