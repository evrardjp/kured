package conditions

import corev1 "k8s.io/api/core/v1"

// Matches checks if node conditions meets the criteria defined by positive and negative conditions.
// It returns true if the node has at least one positive condition set to True and none of the negative conditions set to True.
// In other words: Negative conditions take precedence over positive conditions.
func Matches(conditions []corev1.NodeCondition, positiveConditions, negativeConditions []string) bool {
	// Negative Conditions take precedence over Positive Conditions
	for _, condType := range negativeConditions {
		cond := getNodeCondition(conditions, condType)
		// This block is not necessary. However, it makes the logic explicit: if a negative condition is not present, we ignore it.
		if cond == nil {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return false
		}
	}
	for _, condType := range positiveConditions {
		cond := getNodeCondition(conditions, condType)
		if cond != nil && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func getNodeCondition(conditions []corev1.NodeCondition, condType string) *corev1.NodeCondition {
	for _, cond := range conditions {
		if string(cond.Type) == condType {
			return &cond
		}
	}
	return nil
}
