package shutdown

import (
	"golang.org/x/sys/windows"
)

const (
	SE_SHUTDOWN_NAME = "SeShutdownPrivilege"

	TOKEN_ADJUST_PRIVILEGES = 0x0020
	TOKEN_QUERY             = 0x0008

	SE_PRIVILEGE_ENABLED = 0x00000002

	EWX_POWEROFF = 0x00000008
	EWX_REBOOT   = 0x00000002
	EWX_SHUTDOWN = 0x00000001
)

func enableShutdownPrivilege() error {
	var token windows.Token

	err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		TOKEN_ADJUST_PRIVILEGES|TOKEN_QUERY,
		&token,
	)
	if err != nil {
		return err
	}
	defer token.Close()

	// "SeShutdownPrivilege" -> *uint16
	name, err := windows.UTF16PtrFromString(SE_SHUTDOWN_NAME)
	if err != nil {
		return err
	}

	var luid windows.LUID

	err = windows.LookupPrivilegeValue(
		nil,
		name,
		&luid,
	)
	if err != nil {
		return err
	}

	privileges := windows.Tokenprivileges{
		PrivilegeCount: 1,
	}

	privileges.Privileges[0].Luid = luid
	privileges.Privileges[0].Attributes = SE_PRIVILEGE_ENABLED

	err = windows.AdjustTokenPrivileges(
		token,
		false,
		&privileges,
		0,
		nil,
		nil,
	)
	if err != nil {
		return err
	}

	return nil
}

// PowerOff powers off the machine (no reboot). It enables
// SeShutdownPrivilege on the current process token first; a normal
// (non-elevated) user has the privilege by default on Windows 10/11.
func PowerOff() error {
	if err := enableShutdownPrivilege(); err != nil {
		return err
	}

	return windows.ExitWindowsEx(
		EWX_POWEROFF,
		0,
	)
}
