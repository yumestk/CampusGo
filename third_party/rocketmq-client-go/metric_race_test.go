package golang

import (
	v2 "github.com/apache/rocketmq-clients/golang/v5/protocol/v2"
	"sync"
	"testing"
)

// Regression for v5.1.4: Reset replaces clientMeter while send/receive metric
// interceptors call isEnabled. Both accesses must hold globalMutex.
func TestMetricProviderConcurrentReset(t *testing.T) {
	p := &defaultClientMeterProvider{client: &defaultClient{}, clientMeter: NewDefaultClientMeter(nil, false, nil, "")}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 100 {
			p.Reset(&v2.Metric{On: false})
		}
	}()
	go func() {
		defer wg.Done()
		for range 1000 {
			p.isEnabled()
		}
	}()
	wg.Wait()
}
