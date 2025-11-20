// Package conditions provide a registry of well-known conditions.
package conditions

const (
	// RebootRequiredConditionType is set up by node-reboot-reporter
	RebootRequiredConditionType = "kured.dev/reboot-required"
	// RebootRequiredConditionReason is the reason for the RebootRequired condition
	RebootRequiredConditionReason = "KuredSentinelDetected"
)
const (
	// UnderMaintenanceConditionType is set by maintenance-scheduler when a node matches an active maintenance window, but not necessarily undergoing maintenance yet.
	UnderMaintenanceConditionType   = "kured.dev/under-maintenance-window"
	UnderMaintenanceConditionReason = "KuredFoundMatchingMaintenanceWindowConfigmap"
)

const (
	// InProgressMaintenanceConditionType is set by maintenance-scheduler when a node is undergoing maintenance (e.g., being rebooted).
	InProgressMaintenanceConditionType                            = "kured.dev/maintenance-in-progress"
	InProgressMaintenanceConditionSuccessReason                   = "KuredStartedMaintenance"
	InProgressMaintenanceConditionBadConditionsReason             = "KuredPrerequisitesConditionsUnmet"
	InProgressMaintenanceConditionTooManyNodesInMaintenanceReason = "KuredTooManyNodesCurrentlyInMaintenance"
)

const (
	// InhibitedRebootConditionType is set by reboot-inhibitor to prevent a reboot.
	InhibitedRebootConditionType   = "kured.dev/reboot-inhibited"
	InhibitedRebootConditionReason = "KuredInhibitorDetected"
)
