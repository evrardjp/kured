package maintenances

import "github.com/prometheus/client_golang/prometheus"

var (
	// ActiveWindowsGauge indicates which maintenance windows are currently active
	// (not whether a node is in maintenance)
	ActiveWindowsGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "active_maintenance_windows",
		Help:      "maintenance window is active if its value is 1",
	}, []string{"window"})
	// NodesInProgressGauge indicates which nodes are currently under maintenance
	NodesInProgressGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "maintenance_in_progress_node",
		Help:      "node is under maintenance if this gauge value is 1",
	}, []string{"node"})
)
