// Package metrics даёт единый набор RED-метрик (Rate, Errors, Duration)
// для всех gRPC-сервисов платформы. Именование метрик и лейблов
// стандартизировано, чтобы дашборды в Grafana можно было делать
// один раз по шаблону и переиспользовать для любого сервиса,
// подставляя только job/service лейбл.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// RED — набор метрик, регистрируемых один раз в main() сервиса.
type RED struct {
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	RequestsInFlight *prometheus.GaugeVec
}

// NewRED создаёт и регистрирует стандартные RED-метрики в переданном
// registry. Обычно передаётся prometheus.DefaultRegisterer, но явный
// параметр упрощает тестирование (можно передать пустой registry).
//
// Лейблы одинаковы для всех сервисов: method (полное имя gRPC-метода,
// например /orders.OrderService/GetOrder) и status (grpc status code
// в виде строки, например "OK", "NotFound", "Internal").
func NewRED(reg prometheus.Registerer, namespace string) *RED {
	r := &RED{
		RequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: namespace,
				Name:      "grpc_requests_total",
				Help:      "Общее число обработанных gRPC-запросов.",
			},
			[]string{"method", "status"},
		),
		RequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: namespace,
				Name:      "grpc_request_duration_seconds",
				Help:      "Длительность обработки gRPC-запроса.",
				// Бакеты подобраны под типичный диапазон internal RPC:
				// от 1мс до 10с. Если сервис делает тяжёлые запросы
				// к MSSQL, стоит расширить верхнюю границу.
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			},
			[]string{"method", "status"},
		),
		RequestsInFlight: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: namespace,
				Name:      "grpc_requests_in_flight",
				Help:      "Число запросов, обрабатываемых в данный момент.",
			},
			[]string{"method"},
		),
	}

	reg.MustRegister(r.RequestsTotal, r.RequestDuration, r.RequestsInFlight)
	return r
}
