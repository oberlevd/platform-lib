// Package lifecycle помогает сервисам корректно завершаться по
// SIGTERM/SIGINT: дожидается сигнала от оркестратора, даёт
// зарегистрированным компонентам (gRPC-серверу, HTTP-серверу под
// /metrics, пулу соединений к MSSQL и т.д.) ограниченное время на
// graceful shutdown и логирует ход остановки.
//
// Из коробки пакет НЕ обрабатывает повторные сигналы во время shutdown —
// если во время остановки прилетает второй SIGTERM/SIGINT, k8s всё равно
// через terminationGracePeriodSeconds убьёт под SIGKILL'ом, если
// shutdown не уложился в отведённое время. Это стандартное поведение
// оркестратора, поверх него ничего дополнительно не делаем.
package lifecycle

import (
	"context"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/oberlevd/platform-lib/logger"
)

// ShutdownFunc — компонент, который нужно корректно остановить при
// завершении сервиса. Получает контекст с общим дедлайном на shutdown
// (см. Manager.Run) — обязан уважать его и не блокировать остановку
// дольше отведённого времени.
type ShutdownFunc func(ctx context.Context) error

// Manager собирает компоненты сервиса, которые нужно погасить по
// сигналу, и управляет порядком остановки.
type Manager struct {
	mu    sync.Mutex
	items []namedShutdown
	log   *logger.Logger
}

type namedShutdown struct {
	name string
	fn   ShutdownFunc
}

// New создаёт Manager. log используется для сообщений о ходе shutdown —
// передавайте тот же логгер, что и в остальном сервисе, чтобы записи о
// завершении попали в тот же поток логов с теми же service/env полями.
func New(log *logger.Logger) *Manager {
	return &Manager{log: log}
}

// Register добавляет компонент для остановки. Порядок регистрации — это
// порядок остановки в обратную сторону (по аналогии с defer): то, что
// зарегистрировано последним (обычно самое "верхнеуровневое", например
// gRPC-сервер, принимающий трафик), останавливается первым, а то, от
// чего оно зависит (пул соединений к БД), гасится позже — когда сверху
// уже никто не может прислать новый запрос, которому нужна эта БД.
//
// Типичный порядок регистрации в main(): сначала пул к MSSQL, потом
// gRPC-сервер — тогда остановка пойдёт: сначала gRPC (перестать
// принимать новый трафик и доработать in-flight), потом MSSQL-пул.
func (m *Manager) Register(name string, fn ShutdownFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = append(m.items, namedShutdown{name: name, fn: fn})
}

// Run блокируется до получения SIGINT/SIGTERM (или до отмены ctx —
// удобно в тестах и для программной инициации shutdown), затем
// последовательно, в обратном порядке регистрации, вызывает все
// зарегистрированные ShutdownFunc с общим дедлайном shutdownTimeout.
//
// Ошибки отдельных компонентов логируются, но не прерывают остановку
// остальных — сервис должен погаситься максимально полно, даже если
// что-то одно не смогло корректно завершиться.
func (m *Manager) Run(ctx context.Context, shutdownTimeout time.Duration) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	select {
	case sig := <-sigCh:
		m.log.Info(ctx, "received shutdown signal, starting graceful shutdown", "signal", sig.String())
	case <-ctx.Done():
		m.log.Info(ctx, "context cancelled, starting graceful shutdown")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	m.mu.Lock()
	items := make([]namedShutdown, len(m.items))
	copy(items, m.items)
	m.mu.Unlock()

	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		m.log.Info(shutdownCtx, "stopping component", "component", item.name)
		if err := item.fn(shutdownCtx); err != nil {
			m.log.Error(shutdownCtx, "component failed to shut down cleanly", err, "component", item.name)
		}
	}

	m.log.Info(shutdownCtx, "graceful shutdown complete")
}

// CloserShutdown адаптирует любой io.Closer (например, *sql.DB) под
// ShutdownFunc. io.Closer не принимает контекст, поэтому shutdownTimeout
// не прерывает сам вызов Close() — он либо быстрый (закрытие пула
// соединений обычно так и есть), либо нет, но Manager продолжит работу
// остальных зарегистрированных компонентов сразу после возврата из
// этого вызова, не блокируясь на нём дольше, чем сам Close() выполняется.
func CloserShutdown(c io.Closer) ShutdownFunc {
	return func(ctx context.Context) error {
		return c.Close()
	}
}
