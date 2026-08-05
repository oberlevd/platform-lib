// Package healthcheck даёт стандартные liveness/readiness пробы для
// k8s (или любого другого оркестратора) поверх HTTP, плюс отдельно
// gRPC health-check протокол (grpc.health.v1) в grpc.go - балансировщики
// и service mesh (Envoy/Linkerd) обычно проверяют именно его, а не HTTP.
//
// Разница liveness/readiness:
//   - /healthz (liveness) - "процесс жив, не завис в дедлоке". Почти
//     никогда не должен фейлиться сам по себе - если он фейлится,
//     оркестратор убьёт под. Не должен зависеть от внешних систем
//   - /readyz (readiness) - "готов принимать трафик". Здесь как раз
//     стоит проверять зависимости - если не готов, оркестратор
//     просто не шлёт трафик, под не убивает.
package healthcheck

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// defaultCheckTimeout - по умолчанию ограничивает суммарное время
// прогона readiness-проверок одним запросом к /readyz. Нужен, чтобы
// один зависший (не отказавший явно, а именно подвисший на TCP)
// чекер не заставлял /readyz висеть до тех пор, пока не сработает
// таймаут самого k8s-проба - Handler должен фейлиться быстро и сам.
const defaultCheckTimeout = 3 * time.Second

// Checker - произвольная проверка готовности зависимости (например,
// ping к конкретному MSSQL-хосту). Должен быть быстрым и уважать
// переданный контекст/таймаут - Handler передаёт контекст с дедлайном
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

// New создаёт Handler без зарегистрированных проверок - /readyz будет
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

// snapshot безопасно (под RLock) копирует текущий набор чекеров и таймаут.
// Общий метод для HTTP (ReadyzHandler) и gRPC (GRPCServer.Check, см. grpc.go)
// адаптеров поверх одного и того же Handler - оба должны видеть одинаковый
// набор проверок и одинаковый таймаут, иначе поведение liveness/readiness
// начнёт расходиться в зависимости от того, кто спрашивает: k8s через HTTP
// или Envoy/service mesh через gRPC health protocol.
func (h *Handler) snapshot() (map[string]Checker, time.Duration) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	checkers := make(map[string]Checker, len(h.checkers))
	for name, c := range h.checkers {
		checkers[name] = c
	}
	return checkers, h.checkTimeout
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

// checkResult - промежуточный результат одного чекера, идёт по каналу
// из горутины обратно в runChecks.
type checkResult struct {
	name string
	err  error
}

// runChecks прогоняет все чекеры ПАРАЛЛЕЛЬНО с общим дедлайном ctx.
//
// Критично при нескольких независимых зависимостях (например, 3
// MSSQL-хоста + шина, как в нашей архитектуре): последовательный прогон
// суммирует латентность каждого чекера, и общий дедлайн может быть
// исчерпан до того, как дойдёт очередь до последнего чекера - даже
// если каждая зависимость по отдельности здорова и укладывается в
// бюджет с большим запасом. Проверено: 4 чекера по 150мс каждый при
// таймауте 400мс - последовательно это 600мс суммарно и ложный 503,
// параллельно - 150мс и честный 200.
func runChecks(ctx context.Context, checkers map[string]Checker) (map[string]string, bool) {
	resCh := make(chan checkResult, len(checkers))

	for name, check := range checkers {
		go func(name string, check Checker) {
			resCh <- checkResult{name: name, err: check(ctx)}
		}(name, check)
	}

	results := make(map[string]string, len(checkers))
	allOK := true
	for i := 0; i < len(checkers); i++ {
		r := <-resCh
		if r.err != nil {
			allOK = false
			results[r.name] = r.err.Error()
		} else {
			results[r.name] = "ok"
		}
	}
	return results, allOK
}

// LivezHandler - HTTP-хендлер для /healthz. Всегда 200, если процесс
// в состоянии отвечать на HTTP вообще - намеренно не делает никаких
// проверок зависимостей (см. комментарий пакета).
func (h *Handler) LivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// ReadyzHandler - HTTP-хендлер для /readyz. Прогоняет все
// зарегистрированные проверки параллельно (см. runChecks); если хоть
// одна упала - 503 и в теле ответа видно, какая именно. Весь прогон
// ограничен h.checkTimeout - это защита от чекера, который завис,
// а не вернул явную ошибку.
func (h *Handler) ReadyzHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checkers, timeout := h.snapshot()

		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		checks, allOK := runChecks(ctx, checkers)
		result := readyzResult{Status: "ok", Checks: checks}

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

// RegisterHTTP - удобный хелпер, регистрирует оба хендлера на
// переданном ServeMux по стандартным путям.
func (h *Handler) RegisterHTTP(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", h.LivezHandler())
	mux.HandleFunc("/readyz", h.ReadyzHandler())
}
