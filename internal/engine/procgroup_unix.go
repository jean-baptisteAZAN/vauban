//go:build unix

package engine

import (
	"os"
	"os/exec"
	"syscall"
)

// ownProcessGroup starts c as the leader of a new process group.
func ownProcessGroup(c *exec.Cmd) { c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// signalGroup sends sig to p's process group.
func signalGroup(p *os.Process, sig syscall.Signal) error { return syscall.Kill(-p.Pid, sig) }
