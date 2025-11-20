package maintenances

import "github.com/prometheus/client_golang/prometheus"

var (
	ActiveMaintenanceWindowGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "reboot-daemon",
		Name:      "active_maintenance_windows",
		Help:      "maintenance window is active if its value is 1",
	}, []string{"window"})
)
