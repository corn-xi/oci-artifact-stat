//go:build windows

package ui

import "golang.org/x/sys/windows"

// enableANSI turns on virtual terminal processing for a console handle,
// reporting whether escape sequences will be honoured.
//
// Windows consoles do not interpret ANSI unless an application asks, and
// x/term only enables it for input. Without this, colour would arrive on
// screen as literal escape codes.
func enableANSI(fd int) bool {
	handle := windows.Handle(fd)

	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
