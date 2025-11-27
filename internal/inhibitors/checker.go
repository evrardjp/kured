// Package inhibitors defines the methods and interfaces to deal with node(s) inhibition conditions.
package inhibitors

import (
	"context"
)

// Inhibitor defines the interface for a reboot inhibitor
// An inhibitor checks certain conditions and updates the InhibitedNodeSet accordingly
type Inhibitor interface {
	// Check mutates the InhibitedNodeSet, mentioning block status, reason, and message for a specific node or the overall default.
	// This interface makes it easier to add new inhibitors in the future
	Check(ctx context.Context, inhibitedNodes *InhibitedNodeSet) error
}
