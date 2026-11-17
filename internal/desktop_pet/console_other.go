//go:build !windows

package desktoppet

import "os/exec"

func hideConsoleWindow(command *exec.Cmd) {}
