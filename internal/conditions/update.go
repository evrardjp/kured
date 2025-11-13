package conditions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

func UpdateNodeCondition(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, condition v1.NodeCondition, conditionHeartbeatPeriod time.Duration) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	updateRequired := true
	lastTransitionTime := condition.LastTransitionTime
	for _, c := range node.Status.Conditions {
		if c.Type == condition.Type {
			if c.Status == condition.Status {
				lastTransitionTime = c.LastTransitionTime // keep transition time if the condition's status has not changed
				if metav1.Now().Sub(c.LastHeartbeatTime.Time) <= conditionHeartbeatPeriod {
					// The only reason to skip an update is if the conditiontype is present, the same status as now, and a heartbeat existed recently
					updateRequired = false
				}
			}
			break
		}
	}

	if !updateRequired {
		return nil
	}
	// add retry?
	patch := map[string]interface{}{
		"status": map[string]interface{}{
			"conditions": []map[string]interface{}{
				{
					"type":               condition.Type,
					"status":             condition.Status,
					"reason":             condition.Reason,
					"message":            condition.Message,
					"lastHeartbeatTime":  condition.LastHeartbeatTime,
					"lastTransitionTime": lastTransitionTime,
				},
			},
		},
	}
	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return err

	}
	_, err = clientset.CoreV1().Nodes().Patch(ctx, nodeName, types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{}, "status")
	if err != nil {
		return fmt.Errorf("failed to update node status: %w", err)
	}
	return nil
}

func BoolToConditionStatus(b bool) v1.ConditionStatus {
	if b {
		return v1.ConditionTrue
	}
	return v1.ConditionFalse
}

func StringToConditionType(s string) v1.NodeConditionType {
	return v1.NodeConditionType(s)
}
