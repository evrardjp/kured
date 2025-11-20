package inhibitors

import "github.com/prometheus/client_golang/prometheus"

var (
	RebootInhibitedGauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Subsystem: "kured",
		Name:      "reboot_inhibited",
		Help:      "a node will be prevented reboot if it is set to 1",
	}, []string{"node"})
)
