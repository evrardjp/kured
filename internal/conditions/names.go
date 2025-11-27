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
	UnderMaintenanceConditionType = "kured.dev/under-maintenance-window"
	// UnderMaintenanceConditionReason when there is a configmap matching the maintenance window
	UnderMaintenanceConditionReason = "KuredFoundMatchingMaintenanceWindowConfigmap"
	// UnderNoMaintenanceConditionReason when there is no configmap matching the maintenance window
	UnderNoMaintenanceConditionReason = "KuredFoundNoMatchingMaintenanceWindowConfigmap"
)

const (
	// MaintenanceInProgressConditionType is set by maintenance-scheduler when a node is undergoing maintenance (e.g., being rebooted).
	MaintenanceInProgressConditionType = "kured.dev/maintenance-in-progress"
	// MaintenanceInProgressConditionSuccessReason when maintenance has started
	MaintenanceInProgressConditionSuccessReason = "KuredStartedMaintenance"
	// MaintenanceInProgressConditionBadConditionsReason when prerequisites for maintenance are not met
	MaintenanceInProgressConditionBadConditionsReason = "KuredPrerequisitesConditionsUnmet"
	// MaintenanceInProgressConditionTooManyNodesInMaintenanceReason when too many nodes are already in maintenance
	MaintenanceInProgressConditionTooManyNodesInMaintenanceReason = "KuredTooManyNodesCurrentlyInMaintenance"
)

const (
	// InhibitedRebootConditionType is set by reboot-inhibitor to prevent a reboot.
	InhibitedRebootConditionType = "kured.dev/reboot-inhibited"
	// InhibitedRebootConditionReason when a reboot inhibitor is detected
	InhibitedRebootConditionReason = "KuredInhibitorDetected"
)
