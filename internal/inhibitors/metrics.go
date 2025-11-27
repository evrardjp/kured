package inhibitors

import "github.com/prometheus/client_golang/prometheus"

var (
	// RebootInhibitedGauge indicates whether a node is currently inhibited from rebooting (1) or not (0)
	RebootInhibitedGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "reboot_inhibited",
		Help:      "a node will be prevented reboot if it is set to 1",
	}, []string{"node"})
)
