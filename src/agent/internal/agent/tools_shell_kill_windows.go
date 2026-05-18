//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"

	gopty "github.com/aymanbagabas/go-pty"
)

func shellSetProcessGroup(cmd *exec.Cmd) {}

func shellSetProcessGroupPty(cmd *gopty.Cmd) {}

func shellKillProcessGroup(process *os.Process) {
	if process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", fmt.Sprintf("%d", process.Pid)).Run()
}
