package blockers

import (
	"fmt"
)

// RebootBlocked checks that a single block Checker
// will block the reboot or not.
func RebootBlocked(blockers ...RebootBlocker) (blocked bool, blockernames []string) {
	for _, blocker := range blockers {
		if blocker.IsBlocked() {
			blocked = true
			blockernames = append(blockernames, fmt.Sprintf("%s", blocker))
		}
	}
	return
}

// RebootBlocker interface should be implemented by types
// to know if their instantiations should block a reboot
// As blockers are now exported as labels, the blocker must
// implement a MetricLabel method giving a label for the
// blocker, with low cardinality if possible.
type RebootBlocker interface {
	IsBlocked() bool
	MetricLabel() string
}
