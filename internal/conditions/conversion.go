package conditions

import (
	corev1 "k8s.io/api/core/v1"
)

// BoolToConditionStatus converts a boolean to a corev1.ConditionStatus
// Simple utility function to map true/false to ConditionTrue/ConditionFalse
// instead of using a map.
func BoolToConditionStatus(b bool) corev1.ConditionStatus {
	if b {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

// StringToConditionType converts a string to a corev1.NodeConditionType
// Simple utility function to cast a string to NodeConditionType
func StringToConditionType(s string) corev1.NodeConditionType {
	return corev1.NodeConditionType(s)
}
