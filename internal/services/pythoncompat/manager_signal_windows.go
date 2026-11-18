//go:build windows

package pythoncompat

import (
	"os"
	"os/exec"
	"os/signal"
)

func registerSidecarShutdown() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() {
		<-ch
		stopManagedSidecar()
	}()
}

func terminateProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
