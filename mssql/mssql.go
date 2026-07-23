// Package mssql даёт единый способ открыть пул соединений к MSSQL
// (github.com/microsoft/go-mssqldb, драйвер "sqlserver") с настройками
// пула, таймаутом на первичный коннект и безопасным для логирования
// представлением DSN (без пароля).
//
// Пакет НЕ предоставляет query builder, ORM или обёртки над конкретными
// запросами — это осознанно. Каждый доменный микросервис сам решает,
// какие запросы ему нужны, и параметризует их через database/sql
// (?-плейсхолдеры driver'а sqlserver, см. документацию go-mssqldb).
// platform-lib отвечает только за то, что общее для всех: как открыть
// соединение, как его проверить в /readyz, как не залогировать пароль
// по ошибке.
package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"time"

	_ "github.com/microsoft/go-mssqldb" // регистрирует driver "sqlserver"

	"github.com/oberlevd/platform-lib/healthcheck"
)

// Config — параметры подключения к одному MSSQL-хосту. Секреты (Password)
// приходят из ENV через github.com/oberlevd/platform-lib/config, как и
// остальной конфиг сервиса — см. пример в example/main.go.
type Config struct {
	// Host — адрес MSSQL-инстанса, без порта.
	Host string `env:"MSSQL_HOST,required"`
	// Port — порт MSSQL. 1433 — порт по умолчанию для SQL Server.
	Port int `env:"MSSQL_PORT" default:"1433"`
	// User — логин SQL-аутентификации.
	User string `env:"MSSQL_USER,required"`
	// Password — пароль. Помечен redact:true для единообразия с
	// остальным платформенным конфигом (см. platform-lib/config) —
	// сам package config пока не читает этот тег, но он документирует
	// намерение и совпадает с тем, что реально редактится в логах
	// (см. SafeDSN ниже и platform-lib/logger.redact.go).
	Password string `env:"MSSQL_PASSWORD,required" redact:"true"`
	// Database — имя базы на этом хосте.
	Database string `env:"MSSQL_DATABASE,required"`

	// MaxOpenConns — верхняя граница одновременных соединений к этому
	// хосту. 0 в database/sql означает "без ограничения" — для сервиса,
	// стоящего перед конкретным MSSQL-хостом, это опасно (можно
	// упереться в лимит соединений на самой БД), поэтому здесь всегда
	// есть разумный default.
	MaxOpenConns int `env:"MSSQL_MAX_OPEN_CONNS" default:"20"`
	// MaxIdleConns — сколько простаивающих соединений держать в пуле.
	// Меньше MaxOpenConns, чтобы не держать сверх нужды соединения к
	// БД в простое.
	MaxIdleConns int `env:"MSSQL_MAX_IDLE_CONNS" default:"5"`
	// ConnMaxLifetime — принудительно закрывать соединение старше этого
	// возраста, даже если оно рабочее. Полезно при плановой ротации
	// балансировщика/файловера перед MSSQL и просто чтобы не держать
	// вечные TCP-соединения.
	ConnMaxLifetime time.Duration `env:"MSSQL_CONN_MAX_LIFETIME" default:"5m"`
	// ConnectTimeout — таймаут dial'а до сервера на уровне TDS-протокола
	// (параметр "dial timeout" в DSN). Не путать с таймаутом на Open —
	// это таймаут установления TCP+TDS хэндшейка.
	ConnectTimeout time.Duration `env:"MSSQL_CONNECT_TIMEOUT" default:"5s"`
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
	q.Set("dial timeout", fmt.Sprintf("%d", int(c.ConnectTimeout.Seconds())))
	u.RawQuery = q.Encode()
	return u.String()
}

// SafeDSN возвращает тот же connection string, что и Open использует
// внутри, но с замаскированным паролем — пригодно для логирования при
// отладке проблем подключения (например, "не могу подключиться, вот
// какой DSN использовался"), не боясь утечки пароля в stdout/ELK.
func (c Config) SafeDSN() string {
	safe := c
	safe.Password = "***REDACTED***"
	return safe.dsn()
}

// Open открывает пул соединений к MSSQL согласно cfg, настраивает лимиты
// пула и проверяет, что хотя бы одно соединение реально устанавливается
// (PingContext) — таким образом Open либо возвращает рабочий *sql.DB,
// либо явную ошибку сразу при старте сервиса, а не при первом запросе.
//
// ctx контролирует только ожидание первого Ping — сам *sql.DB продолжает
// жить и переподключаться по мере надобности после Open, ctx с ним не
// связан.
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

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mssql: ping %s: %w", cfg.SafeDSN(), err)
	}

	return db, nil
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
func Checker(db *sql.DB) healthcheck.Checker {
	return func(ctx context.Context) error {
		return db.PingContext(ctx)
	}
}
