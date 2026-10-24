//go:build !darwin && !linux

package cli

// fsTypeOf reports no file system type on a platform without a check.
func fsTypeOf(string) (string, error) { return "unknown", nil }
