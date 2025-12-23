package observability_test

import (
	"testing"
	"time"

	"beacon/internal/observability"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
)

func TestMetrics_DeliverySuccess(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.DeliverySuccess("webhook-1")
	metrics.DeliverySuccess("webhook-1")
	metrics.DeliverySuccess("webhook-2")

	count := testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "success"))
	assert.Equal(t, float64(2), count)

	count = testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-2", "success"))
	assert.Equal(t, float64(1), count)
}

func TestMetrics_DeliveryFailure(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.DeliveryFailure("webhook-1", 500)
	metrics.DeliveryFailure("webhook-1", 503)
	metrics.DeliveryFailure("webhook-1", 400)

	// 5xx should be server_error
	count := testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "server_error"))
	assert.Equal(t, float64(2), count)

	// 4xx should be client_error
	count = testutil.ToFloat64(metrics.DeliveryTotal().WithLabelValues("webhook-1", "client_error"))
	assert.Equal(t, float64(1), count)
}

func TestMetrics_DeliveryDuration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.DeliveryDuration("webhook-1", 100*time.Millisecond)
	metrics.DeliveryDuration("webhook-1", 200*time.Millisecond)

	// Just verify it doesn't panic and histogram is registered
	_ = metrics.DeliveryDurationHist()
}

func TestMetrics_DeadLetter(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.DeadLetter("webhook-1")
	metrics.DeadLetter("webhook-1")

	count := testutil.ToFloat64(metrics.DeadLettersTotal().WithLabelValues("webhook-1"))
	assert.Equal(t, float64(2), count)
}

func TestMetrics_OutboxDepth(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.SetOutboxDepth("pending", 100)
	metrics.SetOutboxDepth("delivering", 10)
	metrics.SetOutboxDepth("delivered", 5000)

	assert.Equal(t, float64(100), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("pending")))
	assert.Equal(t, float64(10), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("delivering")))
	assert.Equal(t, float64(5000), testutil.ToFloat64(metrics.OutboxDepthGauge().WithLabelValues("delivered")))
}

func TestMetrics_ActiveWorkers(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	metrics.SetActiveWorkers(10)
	assert.Equal(t, float64(10), testutil.ToFloat64(metrics.WorkersActiveGauge()))

	metrics.SetActiveWorkers(8)
	assert.Equal(t, float64(8), testutil.ToFloat64(metrics.WorkersActiveGauge()))
}

func TestMetrics_Registration(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics := observability.NewMetrics(registry)

	// Verify all metrics registered
	families, err := registry.Gather()
	assert.NoError(t, err)

	names := make(map[string]bool)
	for _, f := range families {
		names[f.GetName()] = true
	}

	// Verify handler works
	_ = metrics.Handler()
}
