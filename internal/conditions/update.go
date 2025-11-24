package conditions

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// UpsertNodeCondition mutates node and returns true if an update is required
func UpsertNodeCondition(node *corev1.Node, condition corev1.NodeCondition, conditionHeartbeatPeriod time.Duration) bool {
	for i, c := range node.Status.Conditions {
		if c.Type == condition.Type {
			// "i" contains the index of the correct condition to update (or not)
			// now evaluating if I can skip replacing
			if c.Status == condition.Status && metav1.Now().Sub(c.LastHeartbeatTime.Time) <= conditionHeartbeatPeriod {
				return false
			}
			// now evaluating whether to keep lastTransition time or not
			if c.Status == condition.Status {
				condition.LastTransitionTime = c.LastTransitionTime
			}
			// now evaluating to update heartbeat time or not
			if metav1.Now().Sub(c.LastHeartbeatTime.Time) <= conditionHeartbeatPeriod {
				condition.LastHeartbeatTime = c.LastHeartbeatTime
			}
			node.Status.Conditions[i] = condition
			return true
		}
	}
	node.Status.Conditions = append(node.Status.Conditions, condition)
	return true
}

func UpdateNodeCondition(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, condition corev1.NodeCondition, conditionHeartbeatPeriod time.Duration) error {
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
