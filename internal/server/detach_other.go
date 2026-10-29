//go:build !unix

package server

import "syscall"

func detach() *syscall.SysProcAttr { return nil }
