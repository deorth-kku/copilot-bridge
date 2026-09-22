//go:build !windows

package shutdown

// PowerOff is a no-op on non-Windows platforms (the machine-shutdown
// feature is Windows-only for now).
func PowerOff() error { return nil }
