package reboot

import (
	"fmt"
	"log/slog"
	"time"
)

// Rebooter is the standard interface to use to execute
// the reboot, after it has been considered as necessary.
// The Reboot method does not expect any return, yet should
// most likely be refactored in the future to return an error
type Rebooter interface {
	Reboot() error
}

// NewRebooter validates the rebootMethod, rebootCommand, and rebootSignal input,
// then chains to the right constructor.
func NewRebooter(rebootMethod string, rebootCommand string, rebootSignal int, rebootDelay time.Duration, privileged bool, pid int) (Rebooter, error) {
	switch {
	case rebootMethod == "command":
		slog.Info("Will reboot using command", "cmd", rebootCommand)
		return NewCommandRebooter(rebootCommand, rebootDelay, true, 1)
	case rebootMethod == "signal":
		slog.Info("Will reboot using signal", "signal", rebootSignal)
		return NewSignalRebooter(rebootSignal, rebootDelay)
	default:
		return nil, fmt.Errorf("invalid reboot-method configured %s, expected signal or command", rebootMethod)
	}
}

type GenericRebooter struct {
	RebootDelay time.Duration
}

func (g GenericRebooter) DelayReboot() {
	if g.RebootDelay > 0 {
		slog.Debug(fmt.Sprintf("Delayed reboot for %s", g.RebootDelay))
		time.Sleep(g.RebootDelay)
	}
}
