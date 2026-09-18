package main

import (
	"os/exec"
	"syscall"
)

func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// DETACHED_PROCESS: detach from the parent console.
		// CREATE_NO_WINDOW: prevent Windows from creating a visible console
		// window for the child process, so the background server runs
		// silently without a flashing terminal.
		CreationFlags: 0x00000008 | 0x08000000,
	}
}
