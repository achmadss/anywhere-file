//go:build windows

package main

import (
	"os/exec"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32                  = windows.NewLazySystemDLL("kernel32.dll")
	procGetConsoleProcessList = kernel32.NewProc("GetConsoleProcessList")
	procFreeConsole           = kernel32.NewProc("FreeConsole")
)

// detachConsole hides the console window that Windows gives a console program started by
// a scheduled task or a shortcut. It lets go only when nothing else shares the console, so
// the agent run from a terminal keeps printing there.
//
// ponytail: the window still flashes for a moment. Build a second binary with
// -H windowsgui if that turns out to bother people.
func detachConsole() {
	var ids [2]uint32
	if n, _, _ := procGetConsoleProcessList.Call(uintptr(unsafe.Pointer(&ids[0])), 2); n == 1 {
		procFreeConsole.Call()
	}
}

// hideWindow keeps a console program the agent starts from opening a window of its own,
// which Windows gives it once detachConsole has left the agent without one.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
