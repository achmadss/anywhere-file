//go:build !windows

package main

import "os/exec"

// detachConsole and hideWindow are Windows' problem only. See console_windows.go.
func detachConsole() {}

func hideWindow(*exec.Cmd) {}
