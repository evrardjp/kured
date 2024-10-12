package reboot

import (
	"github.com/google/shlex"
	"github.com/kubereboot/kured/pkg/util"
	log "github.com/sirupsen/logrus"
	"time"
)

// CommandRebooter holds context-information for a reboot with command
type CommandRebooter struct {
	RebootCommand []string
	GenericRebooter
}

// Reboot triggers the reboot command
func (c CommandRebooter) Reboot() {
	c.DelayReboot()
	log.Infof("Invoking command: %s", c.RebootCommand)
	if err := util.NewCommand(c.RebootCommand[0], c.RebootCommand[1:]...).Run(); err != nil {
		log.Fatalf("Error invoking reboot command: %v", err)
	}
}

// NewCommandRebooter is the constructor to create a CommandRebooter from a string not
// yet shell lexed. You can skip this constructor if you parse the data correctly first
// when instantiating a CommandRebooter instance.
func NewCommandRebooter(rebootCommand string, rebootDelay time.Duration) *CommandRebooter {
	cmd, err := shlex.Split(rebootCommand)
	if err != nil {
		log.Fatalf("Error parsing provided reboot command: %v", err)
	}

	return &CommandRebooter{
		RebootCommand: util.PrivilegedHostCommand(1, cmd),
		GenericRebooter: GenericRebooter{
			RebootDelay: rebootDelay,
		},
	}
}
