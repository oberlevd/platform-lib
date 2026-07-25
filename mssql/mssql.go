// Package mssql даёт единый способ открыть пул соединений к MSSQL
// (github.com/microsoft/go-mssqldb, драйвер "sqlserver") с настройками
// пула, ретраями на первичное подключение, таймаутом на каждую попытку
// и безопасным для логирования представлением DSN (без пароля).
//
// Пакет НЕ предоставляет query builder, ORM или обёртки над конкретными
// запросами — это осознанно. Каждый доменный микросервис сам решает,
// какие запросы ему нужны, и параметризует их через database/sql
// (?-плейсхолдеры driver'а sqlserver). platform-lib отвечает только за
// то, что общее для всех: как открыть соединение, как его проверить в
// /readyz, как не залогировать пароль по ошибке.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"net/url"
	"time"

	_ "github.com/microsoft/go-mssqldb"

	"github.com/oberlevd/platform-lib/healthcheck"
)

// Значения по умолчанию для полей Config, у которых не задан явный
// default через тег `env:"...,default:..."` (например, если Config
// собирается напрямую как struct literal в коде/тестах, а не через
// config.Load). Совпадают с default-тегами полей ниже.
const (
	defaultRetryAttempts  = 3
	defaultRetryBaseDelay = 200 * time.Millisecond
	defaultRetryMaxDelay  = 2 * time.Second
	defaultConnectTimeout = 5 * time.Second
)

// Config — параметры подключения к одному MSSQL-хосту. Секреты (Password)
// приходят из ENV через github.com/oberlevd/platform-lib/config, как и
// остальной конфиг сервиса — см. пример в example/main.go. Config можно
// встраивать как поле в конфиг конкретного сервиса без собственного
// env-тега на этом поле — config.Load обработает его рекурсивно.
type Config struct {
	// Host — адрес MSSQL-инстанса, без порта.
	Host string `env:"MSSQL_HOST,required"`
	// Port — порт MSSQL. 1433 — порт по умолчанию для SQL Server.
	Port int `env:"MSSQL_PORT" default:"1433"`
	// User — логин SQL-аутентификации.
	User string `env:"MSSQL_USER,required"`
	// Password — пароль. Тег redact:"true" — не просто документация:
	// config.Redacted(&cfg) (см. platform-lib/config) читает этот тег
	// и маскирует значение при логировании стартового конфига сервиса —
	// см. example/main.go. Совпадает с тем, что реально маскируется в
	// логах через SafeDSN ниже и platform-lib/logger.redact.go.
	Password string `env:"MSSQL_PASSWORD,required" redact:"true"`
	// Database — имя базы на этом хосте.
	Database string `env:"MSSQL_DATABASE,required"`

	// MaxOpenConns — верхняя граница одновременных соединений к этому
	// хосту. 0 в database/sql означает "без ограничения" — опасно,
	// поэтому здесь всегда есть разумный default.
	MaxOpenConns int `env:"MSSQL_MAX_OPEN_CONNS" default:"20"`
	// MaxIdleConns — сколько простаивающих соединений держать в пуле.
	MaxIdleConns int `env:"MSSQL_MAX_IDLE_CONNS" default:"5"`
	// ConnMaxLifetime — принудительно закрывать соединение старше этого
	// возраста, даже если оно рабочее.
	ConnMaxLifetime time.Duration `env:"MSSQL_CONN_MAX_LIFETIME" default:"5m"`
	// ConnectTimeout — таймаут на КАЖДУЮ попытку подключения: и на
	// уровне TDS dial timeout в DSN, и как верхняя граница по времени
	// на PingContext одной конкретной попытки в Open (см. ниже).
	ConnectTimeout time.Duration `env:"MSSQL_CONNECT_TIMEOUT" default:"5s"`

	// RetryAttempts — сколько всего попыток подключения сделать в Open,
	// включая первую. MSSQL нередко недоступна короткое время при
	// рестарте пода/файловере — ретраи на старте сервиса избавляют от
	// CrashLoopBackOff из-за транзиентного сбоя длиной в пару секунд.
	RetryAttempts int `env:"MSSQL_CONNECT_RETRY_ATTEMPTS" default:"3"`
	// RetryBaseDelay — задержка перед первым повтором, растёт
	// экспоненциально с full jitter на каждой следующей попытке (см.
	// backoffDelay).
	RetryBaseDelay time.Duration `env:"MSSQL_CONNECT_RETRY_BASE_DELAY" default:"200ms"`
	// RetryMaxDelay — верхняя граница задержки между попытками.
	RetryMaxDelay time.Duration `env:"MSSQL_CONNECT_RETRY_MAX_DELAY" default:"2s"`
}

func (c Config) effectiveConnectTimeout() time.Duration {
	if c.ConnectTimeout > 0 {
		return c.ConnectTimeout
	}
	return defaultConnectTimeout
}

func (c Config) effectiveRetryAttempts() int {
	if c.RetryAttempts > 0 {
		return c.RetryAttempts
	}
	return 1 // без явного значения (например, Config собран как literal
	// в тесте/коде напрямую, минуя config.Load) — не ретраим молча,
	// просто одна попытка, как было раньше.
}

func (c Config) effectiveRetryBaseDelay() time.Duration {
	if c.RetryBaseDelay > 0 {
		return c.RetryBaseDelay
	}
	return defaultRetryBaseDelay
}

func (c Config) effectiveRetryMaxDelay() time.Duration {
	if c.RetryMaxDelay > 0 {
		return c.RetryMaxDelay
	}
	return defaultRetryMaxDelay
}

// dsn собирает connection string в URL-формате, рекомендованном
// go-mssqldb: sqlserver://user:password@host:port?database=...&...
func (c Config) dsn() string {
	u := url.URL{
		Scheme: "sqlserver",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
	}
	q := url.Values{}
	q.Set("database", c.Database)
	q.Set("dial timeout", fmt.Sprintf("%d", int(c.effectiveConnectTimeout().Seconds())))
	u.RawQuery = q.Encode()
	return u.String()
}

// SafeDSN возвращает тот же connection string, что и Open использует
// внутри, но с замаскированным паролем — пригодно для логирования при
// отладке проблем подключения, не боясь утечки пароля в stdout/ELK.
func (c Config) SafeDSN() string {
	safe := c
	safe.Password = "***REDACTED***"
	return safe.dsn()
}

// Open открывает пул соединений к MSSQL согласно cfg, настраивает лимиты
// пула и проверяет, что соединение реально устанавливается (PingContext),
// с ретраями на транзиентные сбои: MSSQL может быть недоступна короткое
// время (файловер, рестарт пода с БД, миграция) — вместо падения сервиса
// с первой попытки Open переживает такие паузы согласно cfg.RetryAttempts.
//
// Каждая попытка ограничена cfg.effectiveConnectTimeout() ИЛИ дедлайном
// переданного ctx — что наступит раньше, так одна зависшая попытка не
// съедает весь бюджет ретраев. Между попытками — экспоненциальный backoff
// с full jitter (см. backoffDelay), с досрочным выходом, если ctx истёк.
//
// Ретраить Ping всегда безопасно (в отличие от произвольных RPC в
// grpcmw — см. предупреждение об идемпотентности в grpcmw/client.go):
// Ping не мутирует данные, поэтому здесь нет аналога WithoutRetry.
func Open(ctx context.Context, cfg Config) (*sql.DB, error) {
	db, err := sql.Open("sqlserver", cfg.dsn())
	if err != nil {
		// В database/sql sql.Open практически никогда не возвращает
		// ошибку сам по себе (не устанавливает соединение) — но
		// оставляем проверку, т.к. интерфейс её допускает.
		return nil, fmt.Errorf("mssql: open %s: %w", cfg.SafeDSN(), err)
	}

	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	attempts := cfg.effectiveRetryAttempts()
	baseDelay := cfg.effectiveRetryBaseDelay()
	maxDelay := cfg.effectiveRetryMaxDelay()
	perAttemptTimeout := cfg.effectiveConnectTimeout()

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		lastErr = db.PingContext(attemptCtx)
		cancel()

		if lastErr == nil {
			return db, nil
		}
		if attempt == attempts {
			break
		}

		delay := backoffDelay(attempt, baseDelay, maxDelay)
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			_ = db.Close()
			return nil, fmt.Errorf("mssql: ping %s: context done during retry backoff: %w (last ping error: %v)", cfg.SafeDSN(), ctx.Err(), lastErr)
		}
	}

	_ = db.Close()
	return nil, fmt.Errorf("mssql: ping %s after %d attempt(s): %w", cfg.SafeDSN(), attempts, lastErr)
}

// maxBackoffShift защищает от переполнения при большом номере попытки.
const maxBackoffShift = 20

// backoffDelay считает задержку перед attempt-й попыткой (attempt >= 1)
// по схеме "full jitter": случайное значение от 0 до min(max, base*2^(attempt-1)).
// Независимая копия того же алгоритма, что и в grpcmw.backoffDelay —
// осознанно не выносим в общий пакет ради пары строк, чтобы не создавать
// связку mssql <-> grpcmw без реальной необходимости.
func backoffDelay(attempt int, base, max time.Duration) time.Duration {
	shift := attempt - 1
	if shift > maxBackoffShift {
		shift = maxBackoffShift
	}

	exp := base * time.Duration(uint64(1)<<uint(shift))
	if exp <= 0 || exp > max {
		exp = max
	}

	return time.Duration(rand.Float64() * float64(exp))
}

// Checker адаптирует *sql.DB под healthcheck.Checker для регистрации в
// /readyz: сервис не готов принимать трафик, если не может достучаться
// до своей MSSQL. name обычно — логическое имя БД (например,
// "mssql-orders-01"), используется только в теле ответа /readyz.
//
// Пример:
//
//	h := healthcheck.New()
//	h.Register("mssql-orders-01", mssql.Checker(db))
//
// Checker НЕ ретраит — readiness-проба должна честно и быстро отражать
// текущее состояние, а не маскировать реальную недоступность зависимости
// повторными попытками (ретраи уместны только на старте в Open).
func Checker(db *sql.DB) healthcheck.Checker {
	return func(ctx context.Context) error {
		return db.PingContext(ctx)
	}
}
