package conditions

import (
	corev1 "k8s.io/api/core/v1"
)

func BoolToConditionStatus(b bool) corev1.ConditionStatus {
	if b {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

func StringToConditionType(s string) corev1.NodeConditionType {
	return corev1.NodeConditionType(s)
}
