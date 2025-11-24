package conditions

import (
	corev1 "k8s.io/api/core/v1"
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
