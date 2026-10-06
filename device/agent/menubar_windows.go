//go:build windows

package main

// The tray icon on Windows (#160), the counterpart of the macOS menu bar item: a laptop in
// the notification area, and a menu with "Open settings" and "Quit" on either click. It is
// Win32 through syscalls, so the agent stays free of cgo here.

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")

	procRegisterClassEx        = user32.NewProc("RegisterClassExW")
	procCreateWindowEx         = user32.NewProc("CreateWindowExW")
	procDefWindowProc          = user32.NewProc("DefWindowProcW")
	procGetMessage             = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessage        = user32.NewProc("DispatchMessageW")
	procRegisterWindowMessage  = user32.NewProc("RegisterWindowMessageW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenu             = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procPostMessage            = user32.NewProc("PostMessageW")
	procCreateIconFromResource = user32.NewProc("CreateIconFromResourceEx")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procShellNotifyIcon        = shell32.NewProc("Shell_NotifyIconW")
)

const (
	wmNull          = 0x0000
	wmSettingChange = 0x001A
	wmCommand       = 0x0111
	wmLButtonUp     = 0x0202
	wmRButtonUp     = 0x0205
	wmTray          = 0x8000 + 1 // WM_APP + 1, what the shell sends clicks on the icon as

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2
	nifIcon   = 0x2
	nifTip    = 0x4
	nifMsg    = 0x1

	tpmReturnCmd   = 0x0100
	tpmRightButton = 0x0002
	smCxSmIcon     = 49

	idOpen = 1
	idQuit = 2
)

type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     uintptr
}

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

type msg struct {
	Wnd     uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

type tray struct {
	wnd            uintptr
	taskbarCreated uintptr
	open, quit     func()
}

func menubarCommand(ctx context.Context, cfg config, log *slog.Logger) error {
	runtime.LockOSThread() // a window's messages arrive on the thread that made it
	t := &tray{open: func() { openSettingsFromMenu(cfg, log) }}
	t.quit = func() {
		t.remove()
		quitFromMenu(cfg, log)
	}
	if err := t.create(); err != nil {
		return err
	}
	// At logon the taskbar may not be up yet. It says TaskbarCreated when it is, and the
	// icon is added then.
	if err := t.show(nimAdd); err != nil {
		log.Warn("no notification area yet, waiting for one", "err", err)
	}
	go func() {
		<-ctx.Done()
		t.remove()
		os.Exit(0)
	}()
	t.loop()
	return nil
}

func (t *tray) create() error {
	name, _ := windows.UTF16PtrFromString("anywhere-file-tray")
	class := wndClassEx{WndProc: windows.NewCallback(t.proc), ClassName: name}
	class.Size = uint32(unsafe.Sizeof(class))
	if r, _, err := procRegisterClassEx.Call(uintptr(unsafe.Pointer(&class))); r == 0 {
		return fmt.Errorf("register the tray window: %w", err)
	}
	// A plain hidden window. A message-only one cannot take the foreground, and without
	// that the menu stays open after a click elsewhere.
	wnd, _, err := procCreateWindowEx.Call(0, uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, 0, 0, 0)
	if wnd == 0 {
		return fmt.Errorf("create the tray window: %w", err)
	}
	t.wnd = wnd
	created, _ := windows.UTF16PtrFromString("TaskbarCreated")
	t.taskbarCreated, _, _ = procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(created)))
	return nil
}

func (t *tray) data() notifyIconData {
	d := notifyIconData{Wnd: t.wnd, ID: 1}
	d.Size = uint32(unsafe.Sizeof(d))
	return d
}

// show adds the icon, or with nimModify redraws it in the taskbar's current colour.
func (t *tray) show(op uintptr) error {
	icon, err := laptopIcon()
	if err != nil {
		return err
	}
	d := t.data()
	d.Flags = nifIcon | nifTip | nifMsg
	d.CallbackMessage = wmTray
	d.Icon = icon
	copy(d.Tip[:], windows.StringToUTF16("anywhere-file"))
	if r, _, err := procShellNotifyIcon.Call(op, uintptr(unsafe.Pointer(&d))); r == 0 {
		return fmt.Errorf("Shell_NotifyIcon: %w", err)
	}
	return nil
}

// remove takes the icon away at once. A process that just ends leaves it in the
// notification area until the pointer passes over it.
func (t *tray) remove() {
	d := t.data()
	procShellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&d)))
}

func (t *tray) loop() {
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func (t *tray) proc(wnd, message, wparam, lparam uintptr) uintptr {
	switch {
	case message == wmTray && (lparam == wmLButtonUp || lparam == wmRButtonUp):
		t.menu()
		return 0
	case message == t.taskbarCreated && t.taskbarCreated != 0:
		_ = t.show(nimAdd) // explorer restarted, and its new taskbar starts empty
	case message == wmSettingChange:
		_ = t.show(nimModify) // the taskbar may have changed between light and dark
	}
	r, _, _ := procDefWindowProc.Call(wnd, message, wparam, lparam)
	return r
}

func (t *tray) menu() {
	m, _, _ := procCreatePopupMenu.Call()
	defer procDestroyMenu.Call(m)
	for _, item := range []struct {
		id    uintptr
		label string
	}{{idOpen, "Open settings"}, {idQuit, "Quit"}} {
		label, _ := windows.UTF16PtrFromString(item.label)
		procAppendMenu.Call(m, 0, item.id, uintptr(unsafe.Pointer(label)))
	}
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Both calls are what Windows asks of a tray menu, or it neither closes on a click
	// elsewhere nor opens twice in a row.
	procSetForegroundWindow.Call(t.wnd)
	cmd, _, _ := procTrackPopupMenu.Call(m, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, t.wnd, 0)
	procPostMessage.Call(t.wnd, wmNull, 0, 0)
	switch cmd {
	case idOpen:
		t.open()
	case idQuit:
		t.quit()
	}
}

// laptopIcon draws the laptop from Figma's "ProductName icon" at the size the notification
// area uses, dark on a light taskbar and white on a dark one.
func laptopIcon() (uintptr, error) {
	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if size == 0 {
		size = 16
	}
	colour := [3]byte{0xFF, 0xFF, 0xFF}
	if lightTaskbar() {
		colour = [3]byte{0x1D, 0x1B, 0x20}
	}
	res := iconResource(int(size), colour)
	h, _, err := procCreateIconFromResource.Call(uintptr(unsafe.Pointer(&res[0])), uintptr(len(res)), 1, 0x00030000, size, size, 0)
	if h == 0 {
		return 0, fmt.Errorf("make the tray icon: %w", err)
	}
	return h, nil
}

func lightTaskbar() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}

// iconResource is an icon in the form an .ico file holds one: a header, the colour rows
// bottom up with their alpha, and a mask that the alpha makes redundant.
func iconResource(size int, rgb [3]byte) []byte {
	maskRow := (size + 31) / 32 * 4
	b := make([]byte, 40, 40+size*size*4+maskRow*size)
	binary.LittleEndian.PutUint32(b[0:], 40)
	binary.LittleEndian.PutUint32(b[4:], uint32(size))
	binary.LittleEndian.PutUint32(b[8:], uint32(size*2)) // colour and mask, stacked
	binary.LittleEndian.PutUint16(b[12:], 1)
	binary.LittleEndian.PutUint16(b[14:], 32)
	for y := size - 1; y >= 0; y-- {
		for x := 0; x < size; x++ {
			b = append(b, rgb[2], rgb[1], rgb[0], laptopCoverage(x, y, size))
		}
	}
	return append(b, make([]byte, maskRow*size)...)
}

// laptopCoverage is how much of pixel (x, y) the laptop covers, 0 to 255, sampled 4 by 4
// so the edges stay smooth at 16 px. The shape is Figma's 24 unit path without its 2 unit
// corner rounding, which is under a pixel at this size: a screen frame and a base.
func laptopCoverage(x, y, size int) byte {
	in := func(u, v float64) bool {
		screen := u >= 2 && u <= 22 && v >= 4 && v <= 18 && !(u > 4 && u < 20 && v > 6 && v < 16)
		base := v >= 18 && v <= 20
		return screen || base
	}
	hits := 0
	for i := 0; i < 4; i++ {
		for j := 0; j < 4; j++ {
			u := (float64(x) + (float64(i)+0.5)/4) * 24 / float64(size)
			v := (float64(y) + (float64(j)+0.5)/4) * 24 / float64(size)
			if in(u, v) {
				hits++
			}
		}
	}
	return byte(hits * 255 / 16)
}
