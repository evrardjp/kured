package detectors

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubereboot/kured/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// Config holds the configuration for the MaintainRebootRequiredCondition controller.
type Config struct {
	Period         time.Duration
	RebootDetector RebootRequiredChecker
	NodeID         string
	Client         *kubernetes.Clientset
}

// MaintainRebootRequiredCondition periodically checks if a reboot is required and updates
// the corresponding node condition in Kubernetes.
func MaintainRebootRequiredCondition(ctx context.Context, config *Config) {
	for range time.Tick(config.Period) {
		rebootRequired := config.RebootDetector.Check()
		slog.Debug("Reboot required check", "result", rebootRequired)
		if rebootRequired {
			rebootRequiredGauge.WithLabelValues(config.NodeID).Set(1)
		} else {
			rebootRequiredGauge.WithLabelValues(config.NodeID).Set(0)
		}
		nodeCondition := corev1.NodeCondition{
			Type:               conditions.StringToConditionType(conditions.RebootRequiredConditionType),
			Status:             conditions.BoolToConditionStatus(rebootRequired),
			Reason:             conditions.RebootRequiredConditionReason,
			Message:            fmt.Sprintf("Kured sentinel check result is %t", rebootRequired),
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
		}
		if err := conditions.UpdateNodeCondition(ctx, config.Client, config.NodeID, nodeCondition, config.Period); err != nil {
			slog.Error("failed to update node condition", "error", err)
		}
	}
}
