//go:build windows

package transport

import (
	"os"
	"os/exec"
)

// setSysProcAttr is a no-op on Windows; process groups are not used.
func setSysProcAttr(cmd *exec.Cmd) {
	// No process group handling on Windows.
	cmd.Cancel = func() error {
		return cmd.Process.Kill()
	}
}

// killProcessGroup kills the child process directly on Windows.
func killProcessGroup(cmd *exec.Cmd, _ os.Signal) {
	_ = cmd.Process.Kill()
}

// processSignals returns the OS signals that should trigger child termination.
func processSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
