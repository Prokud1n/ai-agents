//go:build unix

package extract

import (
	"os/exec"
	"syscall"
)

// killProcessGroup starts the child in its own process group and kills the whole group on cancel,
// so nothing the child might start outlives it.
func killProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
