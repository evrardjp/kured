package conditions

import (
	"context"
	"fmt"

	"k8s.io/api/core/v1"
	v2 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

func UpdateNodeCondition(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, condition v1.NodeCondition) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, v2.GetOptions{})
	if err != nil {
		return err
	}

	updated := false
	for i, c := range node.Status.Conditions {
		if c.Type == condition.Type {
			// Preserve the original transition time if the status hasn't changed.
			if c.Status == condition.Status {
				condition.LastTransitionTime = c.LastTransitionTime
			}
			node.Status.Conditions[i] = condition
			updated = true
			break
		}
	}
	if !updated {
		node.Status.Conditions = append(node.Status.Conditions, condition)
	}

	_, err = clientset.CoreV1().Nodes().UpdateStatus(ctx, node, v2.UpdateOptions{})
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
