//go:build unix

package server

import "syscall"

// detach puts the engine in its own session. Without it, closing the terminal
// (SIGHUP to the foreground process group) would kill the engine while the
// system proxy still points at it, and the machine would lose its network.
func detach() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
