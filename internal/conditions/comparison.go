package conditions

import corev1 "k8s.io/api/core/v1"

// Matches checks if node conditions meets the criteria defined by positive and negative conditions.
// It returns true if the node has ALL required conditions set to True and none of the negative conditions set to True.
// In other words: Negative conditions take precedence over positive conditions.
func Matches(conditions []corev1.NodeCondition, requiredConditionTypes, forbiddenConditionTypes []string) (bool, string) {
	for _, condType := range forbiddenConditionTypes {
		cond := getNodeCondition(conditions, condType)
		// This block is not necessary. However, it makes the logic explicit: if a negative condition is not present, we ignore it.
		if cond == nil {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return false, condType
		}
	}
	for _, condType := range requiredConditionTypes {
		cond := getNodeCondition(conditions, condType)
		if cond == nil {
			return false, condType
		}
		if cond.Status == corev1.ConditionFalse {
			return false, condType
		}

	}
	return true, ""
}

func getNodeCondition(conditions []corev1.NodeCondition, condType string) *corev1.NodeCondition {
	for _, cond := range conditions {
		if string(cond.Type) == condType {
			return &cond
		}
	}
	return nil
}
