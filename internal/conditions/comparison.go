package conditions

import corev1 "k8s.io/api/core/v1"

// Matches checks if node conditions meets the criteria defined by positive and negative conditions.
// It returns a tuple with:
// first value is a boolean, true if the node has ALL required conditions set to True and none of the negative conditions set to True
// second value contains the conditionType, should the first boolean be false.
// In other words: Negative conditions take precedence over positive conditions.
func Matches(conditions []corev1.NodeCondition, requiredConditionTypes, forbiddenConditionTypes []string) (bool, *corev1.NodeCondition) {
	for _, condType := range forbiddenConditionTypes {
		cond := GetNodeCondition(conditions, condType)
		// This block is not necessary. However, it makes the logic explicit: if a negative condition is not present, we ignore it.
		if cond == nil {
			continue
		}
		if cond.Status == corev1.ConditionTrue {
			return false, cond
		}
	}
	for _, condType := range requiredConditionTypes {
		cond := GetNodeCondition(conditions, condType)
		if cond == nil {
			return false, nil
		}
		if cond.Status == corev1.ConditionFalse {
			return false, cond
		}

	}
	return true, nil
}

func GetNodeCondition(conditions []corev1.NodeCondition, condType string) *corev1.NodeCondition {
	for _, cond := range conditions {
		if string(cond.Type) == condType {
			return &cond
		}
	}
	return nil
}
