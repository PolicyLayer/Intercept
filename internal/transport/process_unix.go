//go:build !windows

package transport

import (
	"os"
	"os/exec"
	"syscall"
)

// setSysProcAttr configures the child process to run in its own process group
// so that signals can be delivered to the entire group.
func setSysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

// killProcessGroup sends the given signal to the child's entire process group.
func killProcessGroup(cmd *exec.Cmd, sig os.Signal) {
	_ = syscall.Kill(-cmd.Process.Pid, sig.(syscall.Signal))
}

// processSignals returns the OS signals that should be forwarded to the child.
func processSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT}
}
