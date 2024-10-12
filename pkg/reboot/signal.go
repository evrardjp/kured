package reboot

import (
	"os"
	"syscall"
	"time"

	log "github.com/sirupsen/logrus"
)

// SignalRebooter holds context-information for a signal reboot.
type SignalRebooter struct {
	Signal int
	GenericRebooter
}

// Reboot triggers the reboot signal
func (c SignalRebooter) Reboot() {
	c.DelayReboot()

	log.Infof("Invoking signal: %v", c.Signal)

	process, err := os.FindProcess(1)
	if err != nil {
		log.Fatalf("Not running on Unix: %v", err)
	}

	err = process.Signal(syscall.Signal(c.Signal))
	// Either PID does not exist, or the signal does not work. Hoping for
	// a decent enough error.
	if err != nil {
		log.Fatalf("Signal of SIGRTMIN+5 failed: %v", err)
	}
}

// NewSignalRebooter is the constructor which sets the signal number.
// The constructor does not yet validate any input. It should be done in a later commit.
func NewSignalRebooter(sig int, rebootDelay time.Duration) *SignalRebooter {
	return &SignalRebooter{
		Signal: sig,
		GenericRebooter: GenericRebooter{
			RebootDelay: rebootDelay,
		},
	}
}
