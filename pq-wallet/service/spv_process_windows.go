//go:build windows

package main

import "syscall"

const (
	winProcessQueryLimited = 0x1000
	winProcessTerminate    = 0x0001
)

func spvProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := syscall.OpenProcess(winProcessQueryLimited, false, uint32(pid))
	if err != nil {
		return false
	}
	_ = syscall.CloseHandle(h)
	return true
}

func spvProcessTerminate(pid int) {
	if pid <= 0 {
		return
	}
	h, err := syscall.OpenProcess(winProcessTerminate, false, uint32(pid))
	if err != nil {
		return
	}
	_ = syscall.TerminateProcess(h, 1)
	_ = syscall.CloseHandle(h)
}
