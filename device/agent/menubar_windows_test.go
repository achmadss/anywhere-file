//go:build windows

package main

import (
	"strings"
	"testing"
)

// The icon is built by hand in the layout CreateIconFromResourceEx reads, so a wrong size
// shows up as no icon at all rather than as an error anyone sees.
func TestTheTrayIconHasTheLayoutWindowsReads(t *testing.T) {
	for size, want := range map[int]int{16: 40 + 16*16*4 + 4*16, 20: 40 + 20*20*4 + 4*20, 32: 40 + 32*32*4 + 4*32} {
		if got := len(iconResource(size, [3]byte{})); got != want {
			t.Errorf("%d px icon is %d bytes, want %d", size, got, want)
		}
	}
	if _, err := laptopIcon(); err != nil {
		t.Errorf("Windows refused the icon: %v", err)
	}
}

// show is left out: a runner may have no taskbar to show the icon in.
func TestTheTrayWindowCanBeMade(t *testing.T) {
	tr := &tray{}
	if err := tr.create(); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "desktop") {
			t.Skipf("this runner has no desktop to make a window on: %v", err)
		}
		t.Fatal(err)
	}
	if tr.wnd == 0 || tr.taskbarCreated == 0 {
		t.Errorf("window %#x, TaskbarCreated %#x, want both set", tr.wnd, tr.taskbarCreated)
	}
}
