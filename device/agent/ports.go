package main

// A port somebody else has (#221). The gateway and the settings page listen on fixed
// ports, and each share's dufs on the port it was given when the folder was shared. Any
// other program can be holding one of them, after a reboot for example.

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"sync/atomic"
	"syscall"
	"time"
)

// portRetry is how long the agent waits before it tries a taken port again. A variable so
// a test does not wait half a minute.
var portRetry = 30 * time.Second

// tellPerson puts a message on the screen. A variable so a test does not.
var tellPerson = notify

// otherUser is what a second account on this PC is told (#222). One agent serves a PC.
// Signing out leaves it serving the LAN, so the port is free only once it stops as well.
const otherUser = "anywhere-file already runs for another user on this PC. Sign out of the account there first, then quit anywhere-file there or remove it."

// toldOtherUser keeps the gateway and the settings page, which both wait, from saying it
// twice.
var toldOtherUser atomic.Bool

// listenWhenFree listens on addr, and waits for it while another program has it. Exiting
// would have the service manager start the agent again every few seconds, and the person
// would see only a settings page that does not open, with the reason in the log. gateway
// is where to ask whether the program is another user's agent.
func listenWhenFree(ctx context.Context, addr, gateway string, log *slog.Logger) (net.Listener, error) {
	told := false
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil || !addrInUse(err) {
			return ln, err
		}
		if !told && agentAnswers(gateway) {
			log.Error("another user's agent has the port, so this one waits for it", "addr", addr, "retry_every", portRetry.String())
			if !toldOtherUser.Swap(true) {
				tellPerson(otherUser)
			}
			told = true
		}
		if !told {
			_, port, _ := net.SplitHostPort(addr)
			holder := portHolder(port)
			msg := fmt.Sprintf("%s is using port %s, which anywhere-file needs. Close it and anywhere-file starts by itself.", holder, port)
			log.Error("the port is taken, so the agent waits for it", "addr", addr, "holder", holder, "retry_every", portRetry.String())
			tellPerson(msg)
			told = true
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(portRetry):
		}
	}
}

// addrInUse is true for a port another socket has. Windows reports its own code for it.
func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || errors.Is(err, syscall.Errno(10048))
}

// agentAnswers is true when an anywhere-file gateway answers on addr. Its discovery
// document is open to anybody, so this finds another user's agent, a process lsof and ss
// do not show us. Nothing secret is sent, so the certificate is not checked.
func agentAnswers(addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	c := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	}}
	resp, err := c.Get("https://" + net.JoinHostPort(host, port) + discoveryPath)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var doc struct {
		DeviceID string `json:"device_id"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&doc)
	return resp.StatusCode == http.StatusOK && err == nil && doc.DeviceID != ""
}

// portHolder names the program listening on port, where the OS makes that cheap to ask.
// A program of another user is often hidden from us, and then it is "another program".
func portHolder(port string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	var pick *regexp.Regexp
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-Fc")
		pick = regexp.MustCompile(`(?m)^c(.+)$`)
	case "windows":
		cmd = exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"(Get-Process -Id (Get-NetTCPConnection -LocalPort "+port+" -State Listen).OwningProcess).ProcessName")
		pick = regexp.MustCompile(`(?m)^(\S.*?)\s*$`)
	default:
		cmd = exec.CommandContext(ctx, "ss", "-Hltnp", "sport = :"+port)
		pick = regexp.MustCompile(`users:\(\("([^"]+)"`)
	}
	hideWindow(cmd)
	out, _ := cmd.Output()
	if m := pick.FindSubmatch(out); m != nil {
		return string(m[1])
	}
	return "another program"
}

// notify shows msg as a desktop notification. It is best effort: a PC with no desktop, or
// none this agent can reach, still has the message in the log.
func notify(msg string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		// The message goes in as an argument, so nothing in it is read as AppleScript.
		cmd = exec.Command("osascript", "-e", "on run argv",
			"-e", `display notification (item 1 of argv) with title "anywhere-file"`, "-e", "end run", msg)
	case "windows":
		// A balloon from a short-lived icon, the one notification PowerShell can show
		// without a module. The message goes in through the environment, so nothing in it
		// is read as a command.
		cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			"Add-Type -AssemblyName System.Windows.Forms; $n = New-Object System.Windows.Forms.NotifyIcon; "+
				"$n.Icon = [System.Drawing.SystemIcons]::Warning; $n.Visible = $true; "+
				"$n.ShowBalloonTip(10000, 'anywhere-file', $env:RFM_NOTICE, 'Warning'); Start-Sleep 10; $n.Dispose()")
		cmd.Env = append(os.Environ(), "RFM_NOTICE="+msg)
	default:
		cmd = exec.Command("notify-send", "anywhere-file", msg)
	}
	hideWindow(cmd)
	if cmd.Start() == nil {
		go func() { _ = cmd.Wait() }()
	}
}

// withFreePort gives a back on a new port when another program has the one it was given.
// Only a command that takes --port, which is how a share runs dufs, can be moved.
func withFreePort(a app) (app, bool) {
	i := slices.Index(a.Command, "--port")
	if i < 0 || i+1 >= len(a.Command) {
		return a, false
	}
	ln, err := net.Listen("tcp", a.Address)
	if err == nil {
		_ = ln.Close()
		return a, false
	}
	if !addrInUse(err) {
		return a, false
	}
	host, _, err := net.SplitHostPort(a.Address)
	if err != nil {
		return a, false
	}
	port, err := freePort()
	if err != nil {
		return a, false
	}
	a.Command = slices.Clone(a.Command)
	a.Command[i+1] = port
	a.Address = net.JoinHostPort(host, port)
	return a, true
}
