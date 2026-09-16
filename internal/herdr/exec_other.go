//go:build !windows

package herdr

import "os/exec"

func setExecProcAttr(cmd *exec.Cmd) {
	// No-op on non-Windows platforms.
}
