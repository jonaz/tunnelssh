package master

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Reasons an agent's connection is refused, the values of
// tunnelssh_agent_rejected_total's reason label.
const (
	// The id already has a session: the 409 before the upgrade, or an
	// agent that lost the race for the id after it.
	rejectDuplicateID = "duplicate_id"
	// The websocket upgrade or the yamux session failed.
	rejectHandshake = "handshake"
)

// metrics are the master's Prometheus metrics. They have a registry of their
// own, not the global one, so each master (and each test) counts its own.
type metrics struct {
	registry    *prometheus.Registry
	connects    prometheus.Counter
	disconnects prometheus.Counter
	rejected    *prometheus.CounterVec
}

func newMetrics(sessions *agentSessions) *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		connects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tunnelssh_agent_connects_total",
			Help: "Agent sessions registered.",
		}),
		disconnects: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tunnelssh_agent_disconnects_total",
			Help: "Registered agent sessions that ended.",
		}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tunnelssh_agent_rejected_total",
			Help: "Agent connections refused, by reason.",
		}, []string{"reason"}),
	}
	// Every reason from the start, so a rate() over them has a zero to
	// start from instead of a series that appears on the first refusal.
	for _, r := range []string{rejectDuplicateID, rejectHandshake} {
		m.rejected.WithLabelValues(r)
	}
	m.registry.MustRegister(
		m.connects, m.disconnects, m.rejected,
		sessionsCollector{sessions},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

var (
	sessionsDesc = prometheus.NewDesc(
		"tunnelssh_agent_sessions",
		"Agents connected now.",
		nil, nil,
	)
	connectedDesc = prometheus.NewDesc(
		"tunnelssh_agent_connected",
		"1 for each agent id connected now; an id that is not connected has no series.",
		[]string{"id"}, nil,
	)
)

// sessionsCollector reads the sessions at each scrape, so the gauges are
// never out of step with /sessions-v1 and an agent that leaves drops out
// of tunnelssh_agent_connected with nothing to clean up.
type sessionsCollector struct {
	sessions *agentSessions
}

func (c sessionsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- sessionsDesc
	ch <- connectedDesc
}

func (c sessionsCollector) Collect(ch chan<- prometheus.Metric) {
	ids := c.sessions.IDs()
	ch <- prometheus.MustNewConstMetric(sessionsDesc, prometheus.GaugeValue, float64(len(ids)))
	for _, id := range ids {
		ch <- prometheus.MustNewConstMetric(connectedDesc, prometheus.GaugeValue, 1, id)
	}
}
