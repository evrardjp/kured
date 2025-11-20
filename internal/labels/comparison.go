package labels

//
//import (
//	"fmt"
//)
//
//// LabelMatcher represents a label matching rule
//type LabelMatcher struct {
//	Key    string
//	Value  string
//	Exists bool // true if we only care about key existence, not value
//}
//
//// Matches checks if labels match required and forbidden label patterns
//func Matches(labels map[string]string, requiredLabels []LabelMatcher, forbiddenLabels []LabelMatcher) (bool, string) {
//	// Check forbidden labels first
//	for _, forbidden := range forbiddenLabels {
//		if forbidden.Exists {
//			// Check if a forbidden key exists
//			if _, exists := labels[forbidden.Key]; exists {
//				return false, fmt.Sprintf("label key '%s' is forbidden", forbidden.Key)
//			}
//		} else {
//			// Check if a forbidden key-value pair exists
//			if value, exists := labels[forbidden.Key]; exists {
//				if value == forbidden.Value {
//					return false, fmt.Sprintf("label '%s=%s' is forbidden", forbidden.Key, forbidden.Value)
//				}
//			}
//		}
//	}
//
//	// Check required labels
//	for _, required := range requiredLabels {
//		if required.Exists {
//			// Check if the required key exists
//			if _, exists := labels[required.Key]; !exists {
//				return false, fmt.Sprintf("required label key '%s' is missing", required.Key)
//			}
//		} else {
//			// Check if the required key-value pair exists
//			if value, exists := labels[required.Key]; !exists || value != required.Value {
//				if !exists {
//					return false, fmt.Sprintf("required label '%s=%s' is missing (key not found)", required.Key, required.Value)
//				}
//				return false, fmt.Sprintf("required label '%s=%s' is missing (wrong value, got '%s')", required.Key, required.Value, value)
//			}
//		}
//	}
//	return true, ""
//}
//
//// RequiredLabel is a helper function to create a required label matcher
//func RequiredLabel(key, value string) LabelMatcher {
//	return LabelMatcher{
//		Key:    key,
//		Value:  value,
//		Exists: false,
//	}
//}
//
//// RequiredLabelKey is a helper function to create a required label key matcher (where only key existence matters)
//func RequiredLabelKey(key string) LabelMatcher {
//	return LabelMatcher{
//		Key:    key,
//		Exists: true,
//	}
//}
//
//// ForbiddenLabel is a helper function to create a forbidden label matcher
//func ForbiddenLabel(key, value string) LabelMatcher {
//	return LabelMatcher{
//		Key:    key,
//		Value:  value,
//		Exists: false,
//	}
//}
//
//// ForbiddenLabelKey is a helper function to create a forbidden label key matcher (where only key existence matters)
//func ForbiddenLabelKey(key string) LabelMatcher {
//	return LabelMatcher{
//		Key:    key,
//		Exists: true,
//	}
//}
