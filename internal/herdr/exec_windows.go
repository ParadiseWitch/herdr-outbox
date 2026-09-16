//go:build windows

package herdr

import (
	"os/exec"
	"syscall"
)

func setExecProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CREATE_NO_WINDOW: prevent Windows from creating a visible console
		// window for the herdr child process.
		CreationFlags: 0x08000000,
	}
}
