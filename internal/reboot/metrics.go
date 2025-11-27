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
	reasonDrainTimeout     blockingReason = "drain_timeout"
	reasonDrainFailed      blockingReason = "drain_failed"
	reasonCordonFailed     blockingReason = "cordon_failed"
	reasonAnnotationFailed blockingReason = "annotation_failed"
	reasonConditionAbsent  blockingReason = "condition_absent"
	reasonRebootFailed     blockingReason = "reboot_failed"
	//ReasonUnknown          blockingReason = "unknown"
)

// RecordReason increments the counter for the given node and blocking reason
// This allows monitoring of events that prevent reboots from occurring
// As multiple reconciles may occur for the same blocking event or multiple times during a time period,
// this counter may increment multiple times. It's not the value that counts, but the fact there was an increase event.
// This help tracks trends over time.
func RecordReason(nodeName string, reason blockingReason) {
	rebootBlockedCounter.WithLabelValues(nodeName, string(reason)).Inc()
}
