//go:build darwin || linux

package agentrunner

import (
	"context"
	"os/exec"
	"testing"
)

func TestConfigureCommandCreatesProcessGroup(t *testing.T) {
	command := exec.CommandContext(context.Background(), "true")
	configureCommand(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid || command.Cancel == nil {
		t.Fatalf("command was not isolated: %#v", command.SysProcAttr)
	}
}
