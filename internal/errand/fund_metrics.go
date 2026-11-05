package errand

import "github.com/prometheus/client_golang/prometheus"

func NewFundMetrics(registry *prometheus.Registry) *prometheus.CounterVec {
	events := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "campusgo_fund_operations_total", Help: "Fund execution, durable resolution, receipt and recovery outcomes."}, []string{"operation", "outcome"})
	registry.MustRegister(events)
	return events
}
func (f *Funds) record(operation, outcome string) {
	if f.Events != nil {
		f.Events.WithLabelValues(operation, outcome).Inc()
	}
}
