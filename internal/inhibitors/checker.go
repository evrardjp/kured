package inhibitors

import (
	"context"
)

type Inhibitor interface {
	// Check mutates the InhibitedNodeSet, mentioning block status, reason, and message for a specific node or the overall default.
	// This interface makes it easier to add new inhibitors in the future
	Check(ctx context.Context, inhibitedNodes *InhibitedNodeSet) error
}
