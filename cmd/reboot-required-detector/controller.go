package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kubereboot/kured/internal/checkers"
	"github.com/kubereboot/kured/internal/conditions"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func maintainRebootRequiredCondition(ctx context.Context, client *kubernetes.Clientset, nodeID string, period time.Duration, rebootChecker checkers.Checker) {
	for range time.Tick(period) {
		rebootRequired := rebootChecker.RebootRequired()
		slog.Debug("Reboot required check", "result", rebootRequired)
		if rebootRequired {
			rebootRequiredGauge.WithLabelValues(nodeID).Set(1)
		} else {
			rebootRequiredGauge.WithLabelValues(nodeID).Set(0)
		}
		nodeCondition := corev1.NodeCondition{
			Type:               conditions.StringToConditionType(conditions.RebootRequiredConditionType),
			Status:             conditions.BoolToConditionStatus(rebootRequired),
			Reason:             conditions.RebootRequiredConditionReason,
			Message:            fmt.Sprintf("Kured sentinel check result is %t", rebootRequired),
			LastHeartbeatTime:  metav1.Now(),
			LastTransitionTime: metav1.Now(),
		}
		if err := conditions.UpdateNodeCondition(ctx, client, nodeID, nodeCondition, period); err != nil {
			slog.Error("failed to update node condition", "error", err)
		}
	}
}
