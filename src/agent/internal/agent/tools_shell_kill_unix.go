//go:build unix

package agent

import (
	"os"
	"os/exec"
	"syscall"
)

func shellSetProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func shellKillProcessGroup(process *os.Process) error {
	if process == nil {
		return nil
	}
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}
