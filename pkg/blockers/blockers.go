package blockers

import "fmt"

// RebootBlocked checks that a single block Checker
// will block the reboot or not.
func RebootBlocked(blockers ...RebootBlocker) (blocked bool, blockernames []string) {
	for _, blocker := range blockers {
		if blocker.IsBlocked() {
			blocked = true
			blockernames = append(blockernames, fmt.Sprintf("%T", blocker))
		}
	}
	return
}

// RebootBlocker interface should be implemented by types
// to know if their instantiations should block a reboot
type RebootBlocker interface {
	IsBlocked() bool
}
