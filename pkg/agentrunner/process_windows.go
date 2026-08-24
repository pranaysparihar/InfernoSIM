//go:build windows

package agentrunner

import "os/exec"

// CommandContext terminates the direct child on Windows. Native job-object
// containment is not used because InfernoSIM ships without cgo or platform
// service dependencies.
func configureCommand(_ *exec.Cmd) {}
