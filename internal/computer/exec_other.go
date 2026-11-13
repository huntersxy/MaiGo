//go:build !windows

package computer

import "os/exec"

func hideWindow(cmd *exec.Cmd) {}
