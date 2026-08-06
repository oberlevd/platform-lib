// Package mongo даёт единый способ открыть клиент к MongoDB
// (go.mongodb.org/mongo-driver) с настройками пула, ретраями на
// первичное подключение, таймаутом на каждую попытку и безопасным
// для логирования представлением URI (без пароля).
//
// Пакет НЕ предоставляет query builder, ODM или обёртки над конкретными
// запросами - это осознанно, по тому же принципу, что и platform-lib/mssql.
// Каждый доменный микросервис сам решает, какие коллекции и запросы ему
// нужны, и работает с ними через *mongo.Collection, полученный из Client.
// platform-lib отвечает только за то, что общее для всех: как открыть
// соединение, как его проверить в /readyz, как не залогировать пароль
// по ошибке и как корректно закрыть соединение при graceful shutdown.
package mongo

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/url"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

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
	defaultMaxPoolSize    = 100
)

// Config - параметры подключения к одному MongoDB-кластеру/хосту.
// Секреты (Password) приходят из ENV через github.com/oberlevd/platform-lib/config,
// как и остальной конфиг сервиса - см. пример в example/main.go и
// platform-lib/mssql.Config, по образцу которого сделан этот тип.
// Config можно встраивать как поле в конфиг конкретного сервиса без
// собственного env-тега на этом поле - config.Load обработает его
// рекурсивно.
type Config struct {
	// Host - адрес MongoDB, без порта. Для нескольких хостов реплика-сета
	// в текущей версии пакета укажите первый seed-хост - остальные члены
	// сет обнаруживаются автоматически через SDAM драйвера после
	// первого подключения (mongodb driver сам делает discovery).
	Host string `env:"MONGO_HOST,required"`
	// Port - порт MongoDB. 27017 - порт по умолчанию.
	Port int `env:"MONGO_PORT" default:"27017"`
	// User - логин для аутентификации (SCRAM).
	User string `env:"MONGO_USER,required"`
	// Password - пароль. Тег redact:"true" - не просто документация:
	// config.Redacted(&cfg) (см. platform-lib/config) читает этот тег
	// и маскирует значение при логировании стартового конфига сервиса.
	// Совпадает с тем, что реально маскируется в логах через SafeURI
	// ниже и platform-lib/logger.redact.go.
	Password string `env:"MONGO_PASS,required" redact:"true"`
	// AuthDB - база, на которой хранятся учётные данные пользователя
	// (authSource). Для приложений на самостоятельно поднятой MongoDB
	// это часто admin, но может совпадать и с рабочей базой.
	AuthDB string `env:"MONGO_AUTH_DB,required"`
	// Database - рабочая база, с которой сервис будет работать через
	// Client.Database(). Если не задана, используется AuthDB - так
	// сохраняется поведение сервисов, где рабочая и auth-база совпадают
	// (например, старый search-sessions-service так и делал).
	Database string `env:"MONGO_DATABASE"`

	// MaxPoolSize - верхняя граница одновременных соединений в пуле
	// драйвера. 0 в driver'е означает "использовать default самого
	// driver'а (100)" - здесь всегда есть явный default того же
	// значения, чтобы поведение было видно из конфига, а не из
	// исходников driver'а. Тип int, а не uint64 (как требует
	// SetMaxPoolSize driver'а), - потому что platform-lib/config.Load
	// не умеет парсить uint64 из ENV; приводим к uint64 в
	// effectiveMaxPoolSize/uses ниже.
	MaxPoolSize int `env:"MONGO_MAX_POOL_SIZE" default:"100"`
	// MinPoolSize - сколько соединений держать прогретыми в пуле.
	MinPoolSize int `env:"MONGO_MIN_POOL_SIZE" default:"0"`
	// ConnectTimeout - таймаут на КАЖДУЮ попытку подключения: и на
	// уровне driver'а (ConnectTimeout клиента), и как верхняя граница
	// по времени на Ping одной конкретной попытки в Open (см. ниже).
	ConnectTimeout time.Duration `env:"MONGO_CONNECT_TIMEOUT" default:"5s"`

	// RetryAttempts - сколько всего попыток подключения сделать в Open,
	// включая первую. MongoDB нередко недоступна короткое время при
	// рестарте пода/файловере реплика-сета - ретраи на старте сервиса
	// избавляют от CrashLoopBackOff из-за транзиентного сбоя длиной
	// в пару секунд.
	RetryAttempts int `env:"MONGO_CONNECT_RETRY_ATTEMPTS" default:"3"`
	// RetryBaseDelay - задержка перед первым повтором, растёт
	// экспоненциально с full jitter на каждой следующей попытке (см.
	// backoffDelay).
	RetryBaseDelay time.Duration `env:"MONGO_CONNECT_RETRY_BASE_DELAY" default:"200ms"`
	// RetryMaxDelay - верхняя граница задержки между попытками.
	RetryMaxDelay time.Duration `env:"MONGO_CONNECT_RETRY_MAX_DELAY" default:"2s"`
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
	// в тесте/коде напрямую, минуя config.Load) - не ретраим молча,
	// просто одна попытка.
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

func (c Config) effectiveMaxPoolSize() uint64 {
	if c.MaxPoolSize > 0 {
		return uint64(c.MaxPoolSize)
	}
	return defaultMaxPoolSize
}

func (c Config) effectiveMinPoolSize() uint64 {
	if c.MinPoolSize > 0 {
		return uint64(c.MinPoolSize)
	}
	return 0
}

// effectiveDatabase - рабочая база: явно заданная Database, иначе AuthDB.
func (c Config) effectiveDatabase() string {
	if c.Database != "" {
		return c.Database
	}
	return c.AuthDB
}

// uri собирает connection string в формате, рекомендованном driver'ом:
// mongodb://user:password@host:port/?authSource=...&...
func (c Config) uri() string {
	u := url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(c.User, c.Password),
		Host:   fmt.Sprintf("%s:%d", c.Host, c.Port),
		Path:   "/",
	}
	q := url.Values{}
	q.Set("authSource", c.AuthDB)
	u.RawQuery = q.Encode()
	return u.String()
}

// SafeURI возвращает тот же connection string, что и Open использует
// внутри, но с замаскированным паролем - пригодно для логирования при
// отладке проблем подключения, не боясь утечки пароля в stdout/ELK.
func (c Config) SafeURI() string {
	safe := c
	safe.Password = "***REDACTED***"
	return safe.uri()
}

// Client - тонкая обёртка над *mongo.Client, фиксирующая рабочую базу
// сервиса (см. Config.Database/AuthDB) и дающая точку расширения под
// платформенные хелперы (Checker, Close), не заставляя каждый сервис
// пересобирать эту логику самостоятельно.
type Client struct {
	raw *mongo.Client
	db  *mongo.Database
}

// Raw возвращает исходный *mongo.Client - на случай, если сервису
// нужны возможности driver'а, не покрытые этой обёрткой (транзакции,
// сессии, работа с несколькими базами и т.д.).
func (c *Client) Raw() *mongo.Client {
	return c.raw
}

// Database возвращает рабочую базу сервиса (Config.Database, либо
// Config.AuthDB, если Database не задана).
func (c *Client) Database() *mongo.Database {
	return c.db
}

// Collection - удобный шорткат для db.Collection(name) на рабочей базе
// сервиса. Домен-специфичные репозитории обычно вызывают именно его.
func (c *Client) Collection(name string) *mongo.Collection {
	return c.db.Collection(name)
}

// Close закрывает соединение с MongoDB. Сигнатура намеренно совпадает
// с platform-lib/lifecycle.ShutdownFunc (func(context.Context) error),
// поэтому Client можно зарегистрировать в lifecycle.Manager напрямую,
// без адаптера вроде lifecycle.CloserShutdown (который рассчитан на
// io.Closer без контекста, как *sql.DB в mssql):
//
//	lc.Register("mongo", client.Close)
func (c *Client) Close(ctx context.Context) error {
	return c.raw.Disconnect(ctx)
}

// Open открывает клиент к MongoDB согласно cfg, настраивает лимиты пула
// и проверяет, что соединение реально устанавливается (Ping с
// readpref.Primary), с ретраями на транзиентные сбои: MongoDB может
// быть недоступна короткое время (файловер реплика-сета, рестарт пода
// с БД, миграция) - вместо падения сервиса с первой попытки Open
// переживает такие паузы согласно cfg.RetryAttempts.
//
// Каждая попытка ограничена cfg.effectiveConnectTimeout() ИЛИ дедлайном
// переданного ctx - что наступит раньше, так одна зависшая попытка не
// съедает весь бюджет ретраев. Между попытками - экспоненциальный
// backoff с full jitter (см. backoffDelay), с досрочным выходом, если
// ctx истёк.
//
// Ретраить Ping всегда безопасно (в отличие от произвольных RPC в
// grpcmw - см. предупреждение об идемпотентности в grpcmw/client.go):
// Ping не мутирует данные, поэтому здесь нет аналога WithoutRetry.
func Open(ctx context.Context, cfg Config) (*Client, error) {
	clientOpts := options.Client().
		ApplyURI(cfg.uri()).
		SetConnectTimeout(cfg.effectiveConnectTimeout()).
		SetMaxPoolSize(cfg.effectiveMaxPoolSize()).
		SetMinPoolSize(cfg.effectiveMinPoolSize())

	raw, err := mongo.Connect(clientOpts)
	if err != nil {
		return nil, fmt.Errorf("mongo: connect %s: %w", cfg.SafeURI(), err)
	}

	attempts := cfg.effectiveRetryAttempts()
	baseDelay := cfg.effectiveRetryBaseDelay()
	maxDelay := cfg.effectiveRetryMaxDelay()
	perAttemptTimeout := cfg.effectiveConnectTimeout()

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, perAttemptTimeout)
		lastErr = raw.Ping(attemptCtx, readpref.Primary())
		cancel()

		if lastErr == nil {
			return &Client{
				raw: raw,
				db:  raw.Database(cfg.effectiveDatabase()),
			}, nil
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
			_ = raw.Disconnect(context.Background())
			return nil, fmt.Errorf("mongo: ping %s: context done during retry backoff: %w (last ping error: %v)", cfg.SafeURI(), ctx.Err(), lastErr)
		}
	}

	_ = raw.Disconnect(context.Background())
	return nil, fmt.Errorf("mongo: ping %s after %d attempt(s): %w", cfg.SafeURI(), attempts, lastErr)
}

// maxBackoffShift защищает от переполнения при большом номере попытки.
const maxBackoffShift = 20

// backoffDelay считает задержку перед attempt-й попыткой (attempt >= 1)
// по схеме "full jitter": случайное значение от 0 до min(max, base*2^(attempt-1)).
// Независимая копия того же алгоритма, что и в mssql.backoffDelay /
// grpcmw.backoffDelay - осознанно не выносим в общий пакет ради пары
// строк, чтобы не создавать связку mongo <-> mssql/grpcmw без реальной
// необходимости.
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

// Checker адаптирует *Client под healthcheck.Checker для регистрации в
// /readyz: сервис не готов принимать трафик, если не может достучаться
// до своей MongoDB. name обычно - логическое имя БД (например,
// "mongo-sessions"), используется только в теле ответа /readyz.
//
// Пример:
//
//	h := healthcheck.New()
//	h.Register("mongo-sessions", mongo.Checker(client))
//
// Checker НЕ ретраит - readiness-проба должна честно и быстро отражать
// текущее состояние, а не маскировать реальную недоступность зависимости
// повторными попытками (ретраи уместны только на старте в Open).
func Checker(c *Client) healthcheck.Checker {
	return func(ctx context.Context) error {
		return c.raw.Ping(ctx, readpref.Primary())
	}
}
