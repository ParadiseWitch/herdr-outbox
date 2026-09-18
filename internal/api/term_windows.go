//go:build windows

package api

import (
	"os/exec"
	"strconv"
	"syscall"
)

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

// processAlive needs a real handle: os.FindProcess always succeeds on Windows and
// its Signal only supports Kill. STILL_ACTIVE is what kernel32 reports for a
// process that is running but not yet reaped.
func processAlive(pid int) bool {
	h, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err := syscall.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

// terminate has no portable signal to deliver, so it asks the process to die.
func terminate(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
	return cmd.Run()
}
