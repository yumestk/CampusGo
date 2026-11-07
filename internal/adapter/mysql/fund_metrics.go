package mysql

import "github.com/prometheus/client_golang/prometheus"

func NewFundMetrics(registry *prometheus.Registry) *prometheus.CounterVec {
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "campusgo_fund_operations_total", Help: "Fund execution, durable resolution, receipt and recovery outcomes."}, []string{"operation", "outcome"})
	registry.MustRegister(events)
	return events
}
func NewFundRecorder(registry *prometheus.Registry) func(string, string) {
	events := NewFundMetrics(registry)
	return func(op, outcome string) { events.WithLabelValues(op, outcome).Inc() }
}
