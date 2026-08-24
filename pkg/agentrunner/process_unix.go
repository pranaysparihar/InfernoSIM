//go:build darwin || linux

package agentrunner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommand isolates the explicit agent command in a process group so
// a case timeout cannot leave descendants running with the simulator proxy
// environment after the parent has been killed.
func configureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
