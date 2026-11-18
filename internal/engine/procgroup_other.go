//go:build !unix

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// vauban targets macOS and Linux; elsewhere only the process itself is signalled.
func ownProcessGroup(*exec.Cmd) {}

func signalGroup(p *os.Process, sig syscall.Signal) error {
	if sig == syscall.SIGKILL {
		return p.Kill()
	}
	return p.Signal(os.Interrupt)
}
