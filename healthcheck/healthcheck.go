// Package healthcheck даёт стандартные liveness/readiness пробы для
// k8s (или любого другого оркестратора) поверх HTTP, плюс отдельно
// gRPC health-check протокол (grpc.health.v1) в grpc.go — балансировщики
// и service mesh (Envoy/Linkerd) обычно проверяют именно его, а не HTTP.
//
// Разница liveness/readiness:
//   - /healthz (liveness) — "процесс жив, не завис в дедлоке". Почти
//     никогда не должен фейлиться сам по себе — если он фейлится,
//     оркестратор убьёт под. Не должен зависеть от внешних систем
//   - /readyz (readiness) — "готов принимать трафик". Здесь как раз
//     стоит проверять зависимости — если не готов, оркестратор
//     просто не шлёт трафик, под не убивает.
package healthcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// defaultCheckTimeout — по умолчанию ограничивает суммарное время
// прогона readiness-проверок одним запросом к /readyz. Нужен, чтобы
// один зависший (не отказавший явно, а именно подвисший на TCP)
// чекер не заставлял /readyz висеть до тех пор, пока не сработает
// таймаут самого k8s-проба — Handler должен фейлиться быстро и сам.
const defaultCheckTimeout = 3 * time.Second

// Checker — произвольная проверка готовности зависимости (например,
// ping к конкретному MSSQL-хосту). Должен быть быстрым и уважать
// переданный контекст/таймаут — Handler передаёт контекст с дедлайном
// (см. checkTimeout), но сам Checker обязан на него реагировать
// (например, прокидывать его в driver-вызов), иначе таймаут Handler'а
// не спасёт от реально зависшего сетевого вызова.
type Checker func(ctx context.Context) error

// Option настраивает Handler при создании через New.
type Option func(*Handler)

// WithCheckTimeout переопределяет таймаут на прогон readiness-проверок
// (по умолчанию defaultCheckTimeout).
func WithCheckTimeout(d time.Duration) Option {
	return func(h *Handler) {
		h.checkTimeout = d
	}
}

// Handler агрегирует liveness и набор readiness-проверок.
type Handler struct {
	mu           sync.RWMutex
	checkers     map[string]Checker
	checkTimeout time.Duration
}

// New создаёт Handler без зарегистрированных проверок — /readyz будет
// отвечать 200 сразу, пока не добавлены проверки через Register.
func New(opts ...Option) *Handler {
	h := &Handler{
		checkers:     make(map[string]Checker),
		checkTimeout: defaultCheckTimeout,
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// Register добавляет именованную readiness-проверку. name используется
// только в теле ответа /readyz, чтобы сразу было видно, какая именно
// зависимость недоступна, не заглядывая в логи.
func (h *Handler) Register(name string, check Checker) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checkers[name] = check
}

type readyzResult struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// LivezHandler — HTTP-хендлер для /healthz. Всегда 200, если процесс
// в состоянии отвечать на HTTP вообще — намеренно не делает никаких
// проверок зависимостей (см. комментарий пакета).
func (h *Handler) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// ReadyzHandler — HTTP-хендлер для /readyz. Прогоняет все
// зарегистрированные проверки; если хоть одна упала — 503 и в теле
// ответа видно, какая именно. Весь прогон ограничен h.checkTimeout —
// это защита от чекера, который завис, а не вернул явную ошибку.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		h.mu.RLock()
		checkers := make(map[string]Checker, len(h.checkers))
		for name, c := range h.checkers {
			checkers[name] = c
		}
		h.mu.RUnlock()

		ctx, cancel := context.WithTimeout(r.Context(), h.checkTimeout)
		defer cancel()

		result := readyzResult{Status: "ok", Checks: make(map[string]string, len(checkers))}
		allOK := true

		for name, check := range checkers {
			if err := check(ctx); err != nil {
				allOK = false
				result.Checks[name] = err.Error()
			} else {
				result.Checks[name] = "ok"
			}
		}

		if !allOK {
			result.Status = "unavailable"
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}
}

// RegisterHTTP — удобный хелпер, регистрирует оба хендлера на
// переданном ServeMux по стандартным путям.
func (h *Handler) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.LivezHandler())
	mux.HandleFunc("/readyz", h.ReadyzHandler())
}
