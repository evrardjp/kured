package reboot

import "github.com/prometheus/client_golang/prometheus"

var (
	rebootBlockedCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Subsystem: "kured",
		Name:      "reboot_blocked_reason",
		Help:      "Reboot required was blocked by events such as PodDisruptionBudgets or other drain issues",
	}, []string{"node", "reason"})
)

func init() {
	prometheus.MustRegister(rebootBlockedCounter)
}

type blockingReason string

const (
	ReasonDrainTimeout     blockingReason = "drain_timeout"
	ReasonDrainFailed      blockingReason = "drain_failed"
	ReasonCordonFailed     blockingReason = "cordon_failed"
	ReasonAnnotationFailed blockingReason = "annotation_failed"
	ReasonConditionAbsent  blockingReason = "condition_absent"
	ReasonRebootFailed     blockingReason = "reboot_failed"
	//ReasonUnknown          blockingReason = "unknown"
)

func RecordReason(nodeName string, reason blockingReason) {
	rebootBlockedCounter.WithLabelValues(nodeName, string(reason)).Inc()
}
