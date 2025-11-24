package conditions

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func ConvertFromNodeConditions(nodeConditions []corev1.NodeCondition) *[]metav1.Condition {
	var conditions []metav1.Condition
	for _, nc := range nodeConditions {
		conditions = append(conditions, metav1.Condition{
			Type: string(nc.Type),
			Status: map[corev1.ConditionStatus]metav1.ConditionStatus{
				corev1.ConditionFalse:   metav1.ConditionFalse,
				corev1.ConditionTrue:    metav1.ConditionTrue,
				corev1.ConditionUnknown: metav1.ConditionUnknown,
			}[nc.Status],
			Reason:             nc.Reason,
			Message:            nc.Message,
			LastTransitionTime: nc.LastTransitionTime,
		})
	}
	return &conditions
}

func ConvertToNodeConditions(metaConditions []metav1.Condition) *[]corev1.NodeCondition {
	var conditions []corev1.NodeCondition
	for _, mc := range metaConditions {
		conditions = append(conditions, corev1.NodeCondition{
			Type: corev1.NodeConditionType(mc.Type),
			Status: map[metav1.ConditionStatus]corev1.ConditionStatus{
				metav1.ConditionFalse:   corev1.ConditionFalse,
				metav1.ConditionTrue:    corev1.ConditionTrue,
				metav1.ConditionUnknown: corev1.ConditionUnknown,
			}[mc.Status],
			Reason:             mc.Reason,
			Message:            mc.Message,
			LastTransitionTime: mc.LastTransitionTime,
		})
	}
	return &conditions
}

func BoolToConditionStatus(b bool) corev1.ConditionStatus {
	if b {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

func StringToConditionType(s string) corev1.NodeConditionType {
	return corev1.NodeConditionType(s)
}
