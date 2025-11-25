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

// GetNodeCondition retrieves a specific node condition from a list of node conditions.
func GetNodeCondition(conditions []corev1.NodeCondition, condType string) *corev1.NodeCondition {
	for _, cond := range conditions {
		if string(cond.Type) == condType {
			return &cond
		}
	}
	return nil
}

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

// UpdateNodeCondition patches the node object using strategic merge on the object, computing it from the current node state instead of using controller-runtime
// TODO: Migrate it to controller-runtime client, using for example SmartPatchingNodeConditions function.
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

//// SmartPatchingNodeConditions patches conditions
//// It will aim to centralise all the patches from the different controllers.
//func SmartPatchingNodeConditions(ctx context.Context, statusClient client.Client, node *corev1.Node, conditionsToApply []corev1.NodeCondition, conditionHeartbeatPeriod time.Duration, logger slog.Logger) (bool, error) {
//	nodeCopy := node.DeepCopy()
//	upToDate := true
//	for _, conditionToApply := range conditionsToApply {
//		if updated := UpsertNodeCondition(nodeCopy, conditionToApply, conditionHeartbeatPeriod); updated {
//			upToDate = false
//		}
//	}
//
//	if !upToDate {
//		patch := client.StrategicMergeFrom(node)
//		if err := statusClient.Status().Patch(ctx, nodeCopy, patch); err != nil {
//			return false, fmt.Errorf("failed to update node condition on node %s: %w", node.Name, err)
//		}
//		return true, nil
//	}
//}
