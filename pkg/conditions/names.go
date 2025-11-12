// Package conditions provide a registry of well-known conditions.
package conditions

const (
	// RebootRequiredConditionType is set up by node-reboot-reporter
	RebootRequiredConditionType = "kured.dev/reboot-required"
	// RebootRequiredConditionReason is the reason for the RebootRequired condition
	RebootRequiredConditionReason = "KuredSentinelDetected"
)
const (
	// UnderMaintenanceConditionType is set by maintenance-scheduler
	UnderMaintenanceConditionType   = "kured.dev/under-maintenance"
	UnderMaintenanceConditionReason = "KuredFoundMatchingMaintenanceWindowConfigmap"
)
