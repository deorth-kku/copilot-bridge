//go:build windows

package workspaces

import "syscall"

// launchProcAttr hides the console window of the spawned CLI on Windows.
func launchProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true}
}
