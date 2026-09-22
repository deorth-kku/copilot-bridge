//go:build !windows

package workspaces

import "syscall"

// launchProcAttr is a no-op on non-Windows platforms: HideWindow is
// Windows-only, so no SysProcAttr is needed to launch the CLI.
func launchProcAttr() *syscall.SysProcAttr { return nil }
