// Package conditions provide a registry of well-known conditions.
package conditions

const (
	// RebootRequiredConditionType is set up by node-reboot-reporter
	RebootRequiredConditionType = "kured.dev/reboot-required"
	// PreventRebootConditionType can be set up by external components to prevent reboots
	PreventRebootConditionType = "kured.dev/prevent-reboot"

	// UnderMaintenanceConditionType is set by node-maintenance-scheduler
	UnderMaintenanceConditionType = "kured.dev/under-maintenance"
)
