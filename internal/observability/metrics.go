// Package observability provides metrics, logging, and health checks for Beacon.
package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics provides Prometheus metrics for Beacon.
type Metrics struct {
	deliveryTotal     *prometheus.CounterVec
	deliveryDuration  *prometheus.HistogramVec
	deliveryAttempts  *prometheus.HistogramVec
	deadLettersTotal  *prometheus.CounterVec
	outboxDepth       *prometheus.GaugeVec
	eventsClaimedTotal prometheus.Counter
	eventsReapedTotal  prometheus.Counter
	workersActive     prometheus.Gauge
	workerHeartbeats  prometheus.Counter
	pollDuration      prometheus.Histogram
	apiRequestsTotal  *prometheus.CounterVec
	apiRequestDuration *prometheus.HistogramVec
}

// NewMetrics creates and registers all metrics.
func NewMetrics(registry prometheus.Registerer) *Metrics {
	m := &Metrics{
		deliveryTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "beacon_delivery_total",
				Help: "Total delivery attempts by destination and status",
			},
			[]string{"destination", "status"},
		),
		deliveryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "beacon_delivery_duration_seconds",
				Help:    "Delivery request duration in seconds",
				Buckets: []float64{.01, .05, .1, .25, .5, 1, 2.5, 5, 10, 30},
			},
			[]string{"destination"},
		),
		deliveryAttempts: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "beacon_delivery_attempts",
				Help:    "Number of attempts before success or DLQ",
				Buckets: []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			},
			[]string{"destination"},
		),
		deadLettersTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "beacon_dead_letters_total",
				Help: "Events sent to dead letter queue",
			},
			[]string{"destination"},
		),
		outboxDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "beacon_outbox_depth",
				Help: "Current number of events in each state",
			},
			[]string{"state"},
		),
		eventsClaimedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "beacon_events_claimed_total",
				Help: "Total events claimed from outbox",
			},
		),
		eventsReapedTotal: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "beacon_events_reaped_total",
				Help: "Total events recovered by reaper",
			},
		),
		workersActive: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "beacon_workers_active",
				Help: "Number of active worker goroutines",
			},
		),
		workerHeartbeats: prometheus.NewCounter(
			prometheus.CounterOpts{
				Name: "beacon_worker_heartbeats_total",
				Help: "Total heartbeats sent",
			},
		),
		pollDuration: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name:    "beacon_poll_duration_seconds",
				Help:    "Time to poll and claim events",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
			},
		),
		apiRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "beacon_api_requests_total",
				Help: "Total HTTP API requests",
			},
			[]string{"method", "path", "status"},
		),
		apiRequestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "beacon_api_request_duration_seconds",
				Help:    "HTTP API request duration",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
			},
			[]string{"method", "path"},
		),
	}

	registry.MustRegister(
		m.deliveryTotal,
		m.deliveryDuration,
		m.deliveryAttempts,
		m.deadLettersTotal,
		m.outboxDepth,
		m.eventsClaimedTotal,
		m.eventsReapedTotal,
		m.workersActive,
		m.workerHeartbeats,
		m.pollDuration,
		m.apiRequestsTotal,
		m.apiRequestDuration,
	)

	return m
}

// Handler returns an HTTP handler for /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.Handler()
}

// DeliverySuccess records a successful delivery.
func (m *Metrics) DeliverySuccess(destination string) {
	m.deliveryTotal.WithLabelValues(destination, "success").Inc()
}

// DeliveryFailure records a failed delivery.
func (m *Metrics) DeliveryFailure(destination string, statusCode int) {
	status := "server_error"
	if statusCode >= 400 && statusCode < 500 {
		status = "client_error"
	}
	m.deliveryTotal.WithLabelValues(destination, status).Inc()
}

// DeliveryTimeout records a timeout failure.
func (m *Metrics) DeliveryTimeout(destination string) {
	m.deliveryTotal.WithLabelValues(destination, "timeout").Inc()
}

// DeliveryConnectionError records a connection error.
func (m *Metrics) DeliveryConnectionError(destination string) {
	m.deliveryTotal.WithLabelValues(destination, "connection_error").Inc()
}

// DeliveryRetry records a retry being scheduled.
func (m *Metrics) DeliveryRetry(destination string) {
	m.deliveryTotal.WithLabelValues(destination, "retry").Inc()
}

// DeadLetter records an event being sent to DLQ.
func (m *Metrics) DeadLetter(destination string) {
	m.deadLettersTotal.WithLabelValues(destination).Inc()
}

// DeliveryDuration records the duration of a delivery attempt.
func (m *Metrics) DeliveryDuration(destination string, duration time.Duration) {
	m.deliveryDuration.WithLabelValues(destination).Observe(duration.Seconds())
}

// DeliveryAttemptsCount records how many attempts it took.
func (m *Metrics) DeliveryAttemptsCount(destination string, attempts int) {
	m.deliveryAttempts.WithLabelValues(destination).Observe(float64(attempts))
}

// SetOutboxDepth sets the current event count for a state.
func (m *Metrics) SetOutboxDepth(state string, count int64) {
	m.outboxDepth.WithLabelValues(state).Set(float64(count))
}

// EventsClaimed records events claimed.
func (m *Metrics) EventsClaimed(count int) {
	m.eventsClaimedTotal.Add(float64(count))
}

// EventsReaped records events recovered by reaper.
func (m *Metrics) EventsReaped(count int) {
	m.eventsReapedTotal.Add(float64(count))
}

// SetActiveWorkers sets the current worker count.
func (m *Metrics) SetActiveWorkers(count int) {
	m.workersActive.Set(float64(count))
}

// WorkerHeartbeat records a heartbeat.
func (m *Metrics) WorkerHeartbeat() {
	m.workerHeartbeats.Inc()
}

// PollDuration records poll duration.
func (m *Metrics) PollDuration(duration time.Duration) {
	m.pollDuration.Observe(duration.Seconds())
}

// APIRequest records an API request.
func (m *Metrics) APIRequest(method, path, status string) {
	m.apiRequestsTotal.WithLabelValues(method, path, status).Inc()
}

// APIRequestDuration records API request duration.
func (m *Metrics) APIRequestDuration(method, path string, duration time.Duration) {
	m.apiRequestDuration.WithLabelValues(method, path).Observe(duration.Seconds())
}

// Getters for testing

// DeliveryTotal returns the delivery total counter vector.
func (m *Metrics) DeliveryTotal() *prometheus.CounterVec {
	return m.deliveryTotal
}

// DeliveryDurationHist returns the delivery duration histogram vector.
func (m *Metrics) DeliveryDurationHist() *prometheus.HistogramVec {
	return m.deliveryDuration
}

// DeadLettersTotal returns the dead letters counter vector.
func (m *Metrics) DeadLettersTotal() *prometheus.CounterVec {
	return m.deadLettersTotal
}

// OutboxDepthGauge returns the outbox depth gauge vector.
func (m *Metrics) OutboxDepthGauge() *prometheus.GaugeVec {
	return m.outboxDepth
}

// WorkersActiveGauge returns the workers active gauge.
func (m *Metrics) WorkersActiveGauge() prometheus.Gauge {
	return m.workersActive
}
