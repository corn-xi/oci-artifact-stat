//go:build !windows

package ui

// enableANSI is a no-op: every other terminal this tool targets interprets
// escape sequences without being asked.
func enableANSI(int) bool { return true }
