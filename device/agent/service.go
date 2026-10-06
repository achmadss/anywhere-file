package main

// The agent as a user-level service (#33). A PC is only useful to its owner when it is
// reachable with nobody sitting at it, so `agent install` hands the binary to whatever
// starts programs on this OS and asks for it back after a reboot: launchd on macOS, a
// logon-triggered scheduled task on Windows, a systemd user unit on Linux.
//
// It stays in the user's own session everywhere. The device key lives in the user's
// keystore, and a machine-wide service cannot read it. On Windows that rules out a real
// service: those run in session 0, with no access to the user's credential store.
//
// A service starts with no shell, so whatever RFM_AGENT_* is set when install runs is
// written into the manifest. Reinstall after changing one.

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"text/template"
	"unicode/utf16"
)

const (
	// launchd wants a reverse DNS label. The other two want something that reads as a
	// filename.
	serviceLabel = "io.anywhere-file.agent"
	menubarLabel = "io.anywhere-file.menubar"
	serviceName  = "anywhere-file-agent"
	trayName     = "anywhere-file-tray"
	envPrefix    = "RFM_AGENT_"
)

// servicePlan is the whole of what install and uninstall do: write these files, run these
// commands, and on the way out run those and delete the files again.
type servicePlan struct {
	files     []planFile
	install   []planCmd
	uninstall []planCmd
	// quit is the tray menu's Quit (#159, #160): sharing and the icon stop, and stay
	// stopped after a restart. reopen undoes it, which is what opening the app again does.
	quit   []planCmd
	reopen []planCmd
}

type planFile struct {
	path string
	body string
	// utf16 writes the file as UTF-16LE with a byte order mark. schtasks refuses a task
	// definition in any other encoding.
	utf16 bool
}

type planCmd struct {
	argv []string
	// optional commands report and carry on. Uninstalling something that was never
	// installed is not a failure, and lingering needs a privilege the user may not have.
	optional bool
}

// planInput is everything the manifests need that the process has to look up.
type planInput struct {
	goos string
	exe  string // the agent binary, an absolute path
	dir  string // the agent's own directory, for the log and for Windows' wrapper
	home string
	user string // Windows only: DOMAIN\user for the task principal
	env  [][2]string
}

func servicePlanFor(in planInput) (servicePlan, error) {
	switch in.goos {
	case "darwin":
		return darwinPlan(in), nil
	case "linux":
		return linuxPlan(in), nil
	case "windows":
		return windowsPlan(in), nil
	}
	return servicePlan{}, fmt.Errorf("no service manifest for %s, run `agent run` from whatever starts programs on it", in.goos)
}

var darwinPlist = template.Must(template.New("plist").Funcs(template.FuncMap{"x": xmlEscape}).Parse(
	`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<!-- #223: an app dragged to the Trash takes the binary and leaves this file. The job
	     then deletes this file and itself, so launchd stops starting a missing program and
	     the next login loads nothing. exec keeps the agent as the process launchd watches. -->
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>[ -x "$1" ] || { rm -f "$0"; exec launchctl remove {{.Label}}; }; exec "$@"</string>
		<string>{{x .Path}}</string>
		<string>{{x .Exe}}</string>
		<string>{{.Command}}</string>
{{- range .Args}}
		<string>{{x .}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<!-- Login Items in System Settings lists every job by its binary's name. This puts
	     both under the app instead, as one entry called anywhere-file. -->
	<key>AssociatedBundleIdentifiers</key>
	<string>{{.Bundle}}</string>
{{- if .Menu}}
	<key>LimitLoadToSessionType</key>
	<string>Aqua</string>
	<key>ProcessType</key>
	<string>Interactive</string>
{{- else}}
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Standard</string>
{{- end}}
	<!-- The agent writes and rotates its own log. What reaches stderr is what comes before
	     the log is open, and a crash. -->
	<key>StandardErrorPath</key>
	<string>{{x .Stderr}}</string>
</dict>
</plist>
`))

func darwinPlan(in planInput) servicePlan {
	agents := filepath.Join(in.home, "Library", "LaunchAgents")
	path := filepath.Join(agents, serviceLabel+".plist")
	// The menu bar item (#159) is a second job, because the agent's own job runs in the
	// background and a status item needs the login window's session. It is not kept
	// alive: Quit in its menu has to stay quit.
	menu := filepath.Join(agents, menubarLabel+".plist")
	plist := func(label, command string) string {
		return render(darwinPlist, map[string]any{
			"Label":   label,
			"Path":    filepath.Join(agents, label+".plist"),
			"Bundle":  serviceLabel, // the app's CFBundleIdentifier, which is the same string
			"Command": command,
			"Menu":    command == "menubar",
			"Exe":     in.exe,
			"Stderr":  filepath.Join(in.dir, "stderr.log"),
			"Args":    envArgs(withLogFile(in.env, in.dir, command)),
		})
	}
	return servicePlan{
		files: []planFile{
			{path: path, body: plist(serviceLabel, "run")},
			{path: menu, body: plist(menubarLabel, "menubar")},
		},
		// ponytail: `load -w` rather than `bootstrap gui/$UID`, because it works the same
		// from a login window session and from a headless one. Move to bootstrap if we
		// ever need to name the domain, for example to install for another user.
		install: []planCmd{
			{argv: []string{"launchctl", "unload", "-w", path}, optional: true},
			{argv: []string{"launchctl", "load", "-w", path}},
			// A Mac reached over ssh has no login window session, so the menu waits for
			// the next logon there.
			{argv: []string{"launchctl", "unload", "-w", menu}, optional: true},
			{argv: []string{"launchctl", "load", "-w", menu}, optional: true},
		},
		// The menu goes last, because when it is the one asking it stops here.
		uninstall: []planCmd{
			{argv: []string{"launchctl", "unload", "-w", path}, optional: true},
			{argv: []string{"launchctl", "unload", "-w", menu}, optional: true},
		},
		// -w writes the off switch down, so neither job comes back at the next logon.
		quit: []planCmd{
			{argv: []string{"launchctl", "unload", "-w", path}, optional: true},
			{argv: []string{"launchctl", "unload", "-w", menu}, optional: true},
		},
		// Optional, because launchctl calls loading a job that already runs a failure.
		reopen: []planCmd{
			{argv: []string{"launchctl", "load", "-w", path}, optional: true},
			{argv: []string{"launchctl", "load", "-w", menu}, optional: true},
		},
	}
}

var linuxUnit = template.Must(template.New("unit").Parse(
	`[Unit]
Description=anywhere-file agent
After=network-online.target

[Service]
ExecStart={{.Exe}} run{{range .Args}} {{.}}{{end}}
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`))

// linuxDesktop puts the settings page in the applications menu (#136). A tray icon would
// be the equivalent of the macOS menu bar item and the Windows tray one, and there is no
// tray on Linux worth depending on: GNOME dropped it in 3.26, the X11 one does not exist
// under Wayland, and what is left is a D-Bus interface only some desktops implement.
var linuxDesktop = template.Must(template.New("desktop").Parse(
	`[Desktop Entry]
Type=Application
Name=anywhere-file settings
Comment=Choose which folders this PC shares
Exec={{.Exe}} settings
Terminal=false
Categories=Network;FileTransfer;Settings;
`))

func linuxPlan(in planInput) servicePlan {
	unit := serviceName + ".service"
	path := filepath.Join(in.home, ".config", "systemd", "user", unit)
	body := render(linuxUnit, map[string]any{"Exe": in.exe, "Args": quoted(envArgs(in.env))})
	desktop := filepath.Join(in.home, ".local", "share", "applications", serviceName+"-settings.desktop")
	return servicePlan{
		files: []planFile{
			{path: path, body: body},
			{path: desktop, body: render(linuxDesktop, map[string]any{"Exe": in.exe})},
		},
		install: []planCmd{
			// Without lingering the user manager stops at logout and takes the agent with
			// it. It needs a privilege the user may not have, so a refusal is a warning:
			// the service still runs, it just will not survive a reboot on its own.
			{argv: []string{"loginctl", "enable-linger"}, optional: true},
			{argv: []string{"systemctl", "--user", "daemon-reload"}},
			{argv: []string{"systemctl", "--user", "enable", "--now", unit}},
		},
		uninstall: []planCmd{
			{argv: []string{"systemctl", "--user", "disable", "--now", unit}, optional: true},
			{argv: []string{"systemctl", "--user", "daemon-reload"}, optional: true},
		},
	}
}

var windowsTask = template.Must(template.New("task").Funcs(template.FuncMap{"x": xmlEscape}).Parse(
	`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>anywhere-file agent</Description>
  </RegistrationInfo>
  <Triggers>
    <LogonTrigger>
      <Enabled>true</Enabled>
{{- if .User}}
      <UserId>{{x .User}}</UserId>
{{- end}}
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
{{- if .User}}
      <UserId>{{x .User}}</UserId>
{{- end}}
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
{{- if .Restart}}
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
{{- end}}
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>{{x .Exe}}</Command>
      <Arguments>{{x .Arguments}}</Arguments>
    </Exec>
  </Actions>
</Task>
`))

func windowsPlan(in planInput) servicePlan {
	task := filepath.Join(in.dir, "service", serviceName+".xml")
	// The tray icon (#160) is a second task, because it needs the desktop of whoever
	// logged on. It is not restarted: Quit in its menu has to stay quit.
	tray := filepath.Join(in.dir, "service", trayName+".xml")
	file := func(path, command string) planFile {
		return planFile{path: path, utf16: true, body: render(windowsTask, map[string]any{
			"User":      in.user,
			"Exe":       in.exe,
			"Arguments": strings.Join(append([]string{command}, quoted(envArgs(withLogFile(in.env, in.dir, command)))...), " "),
			"Restart":   command == "run",
		})}
	}
	schtasks := func(optional bool, args ...string) planCmd {
		return planCmd{argv: append([]string{"schtasks"}, args...), optional: optional}
	}
	return servicePlan{
		files: []planFile{file(task, "run"), file(tray, "menubar")},
		install: []planCmd{
			schtasks(false, "/Create", "/TN", serviceName, "/XML", task, "/F"),
			schtasks(false, "/Create", "/TN", trayName, "/XML", tray, "/F"),
			// The trigger is a logon that has already happened, so the first start is ours.
			schtasks(false, "/Run", "/TN", serviceName),
			// A PC reached over ssh has no desktop, so the icon waits for the next logon.
			schtasks(true, "/Run", "/TN", trayName),
		},
		// The tray goes last, because when it is the one asking it stops here.
		uninstall: []planCmd{
			schtasks(true, "/End", "/TN", serviceName),
			schtasks(true, "/Delete", "/TN", serviceName, "/F"),
			schtasks(true, "/End", "/TN", trayName),
			schtasks(true, "/Delete", "/TN", trayName, "/F"),
		},
		// The tray's own task is only switched off, since the tray is the one asking and
		// exits by itself.
		quit: []planCmd{
			schtasks(true, "/End", "/TN", serviceName),
			schtasks(true, "/Change", "/TN", serviceName, "/DISABLE"),
			schtasks(true, "/Change", "/TN", trayName, "/DISABLE"),
		},
		reopen: []planCmd{
			schtasks(false, "/Change", "/TN", serviceName, "/ENABLE"),
			schtasks(true, "/Change", "/TN", trayName, "/ENABLE"),
			schtasks(false, "/Run", "/TN", serviceName),
			schtasks(true, "/Run", "/TN", trayName),
		},
	}
}

// envArgs is how the settings reach an installed service: as arguments, because the one
// place all three schedulers agree on is the command line.
func envArgs(env [][2]string) []string {
	args := make([]string, 0, len(env))
	for _, kv := range env {
		args = append(args, kv[0]+"="+kv[1])
	}
	return args
}

// quoted wraps what a shell or a Windows command line would otherwise split in two.
func quoted(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if strings.ContainsAny(a, " \t") {
			a = `"` + a + `"`
		}
		out = append(out, a)
	}
	return out
}

// withLogFile tells a job where to keep its log, unless one was chosen at install. The
// agent writes the file itself on macOS and Windows so it can rotate it (#220): the
// scheduler captures nothing, and launchd would keep the file open under its old name.
// Each job gets its own file, because Windows will not rename a file another process has
// open. Linux has the journal.
func withLogFile(env [][2]string, dir, command string) [][2]string {
	if hasKey(env, "RFM_AGENT_LOG_FILE") {
		return env
	}
	name := "agent.log"
	if command == "menubar" {
		name = "menubar.log"
	}
	env = append(slices.Clone(env), [2]string{"RFM_AGENT_LOG_FILE", filepath.Join(dir, name)})
	sort.Slice(env, func(i, j int) bool { return env[i][0] < env[j][0] })
	return env
}

func hasKey(env [][2]string, key string) bool {
	for _, kv := range env {
		if kv[0] == key {
			return true
		}
	}
	return false
}

// currentPlan reads what this machine has to offer the manifests.
func currentPlan(cfg config) (servicePlan, error) {
	exe, err := os.Executable()
	if err != nil {
		return servicePlan{}, fmt.Errorf("this binary's own path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if temporaryBuild(exe, os.TempDir()) {
		return servicePlan{}, fmt.Errorf("%s is a temporary build, and the OS would point the service at a path that disappears. Build the agent first: go build -o agent ./device/agent", exe)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return servicePlan{}, err
	}
	in := planInput{
		goos: runtime.GOOS,
		exe:  exe,
		dir:  cfg.dir,
		home: home,
		env:  serviceEnv(os.Environ()),
	}
	if in.goos == "windows" {
		if u, d := os.Getenv("USERNAME"), os.Getenv("USERDOMAIN"); u != "" {
			in.user = u
			if d != "" {
				in.user = d + `\` + u
			}
		}
	}
	return servicePlanFor(in)
}

// temporaryBuild reports whether the running binary is one `go run` made. Those live in a
// directory that is deleted when the command ends, so a service pointed at one starts
// once and then fails forever with a file that is not there.
func temporaryBuild(exe, tmp string) bool {
	if tmp == "" {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = resolved
	}
	rel, err := filepath.Rel(tmp, exe)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// serviceEnv picks the agent's own settings out of the environment. Anything else the
// shell happens to hold is none of the service's business.
func serviceEnv(environ []string) [][2]string {
	var env [][2]string
	for _, e := range environ {
		key, value, ok := strings.Cut(e, "=")
		if ok && strings.HasPrefix(key, envPrefix) {
			env = append(env, [2]string{key, value})
		}
	}
	sort.Slice(env, func(i, j int) bool { return env[i][0] < env[j][0] })
	return env
}

func installService(cfg config, log *slog.Logger, out io.Writer) error {
	p, err := currentPlan(cfg)
	if err != nil {
		return err
	}
	// The agent's own directory has to exist before a log can be written into it, and on a
	// PC installed by hand it may not yet.
	if err := os.MkdirAll(cfg.dir, 0o700); err != nil {
		return err
	}
	for _, f := range p.files {
		if err := writePlanFile(f); err != nil {
			return err
		}
		fmt.Fprintln(out, "wrote", f.path)
	}
	if err := runPlan(p.install, log, out); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s is installed and running. Logs: %s\n", serviceName, filepath.Join(cfg.dir, "agent.log"))
	return nil
}

func uninstallService(cfg config, log *slog.Logger, out io.Writer) error {
	p, err := currentPlan(cfg)
	if err != nil {
		return err
	}
	if err := runPlan(p.uninstall, log, out); err != nil {
		return err
	}
	for _, f := range p.files {
		if err := os.Remove(f.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Fprintln(out, "removed", f.path)
	}
	// The key and the registry are the user's, and an uninstall that takes the identity
	// with it turns a reinstall into a new device.
	fmt.Fprintf(out, "%s is gone. The device key and %s are untouched.\n", serviceName, cfg.dir)
	return nil
}

func writePlanFile(f planFile) error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	body := []byte(f.body)
	if f.utf16 {
		body = toUTF16LE(f.body)
	}
	return os.WriteFile(f.path, body, 0o600)
}

func runPlan(cmds []planCmd, log *slog.Logger, out io.Writer) error {
	for _, c := range cmds {
		argv := c.argv
		// loginctl needs to be told who, and only the process knows.
		if argv[0] == "loginctl" && len(argv) == 2 {
			argv = append(argv, currentUser())
		}
		cmd := exec.Command(argv[0], argv[1:]...)
		hideWindow(cmd)
		output, err := cmd.CombinedOutput()
		trimmed := strings.TrimSpace(string(output))
		if err != nil {
			if !c.optional {
				return fmt.Errorf("%s: %w: %s", strings.Join(argv, " "), err, trimmed)
			}
			log.Warn("service step skipped", "cmd", strings.Join(argv, " "), "err", err, "output", trimmed)
			continue
		}
		if trimmed != "" {
			fmt.Fprintln(out, trimmed)
		}
	}
	return nil
}

func currentUser() string {
	for _, key := range []string{"USER", "LOGNAME", "USERNAME"} {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func render(t *template.Template, data any) string {
	var b bytes.Buffer
	// The templates are constants and the data is strings, so the only error left is a
	// write error to a bytes.Buffer, which does not happen.
	_ = t.Execute(&b, data)
	return b.String()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// toUTF16LE encodes with a byte order mark. schtasks reads a task definition as Unicode
// and reports a malformed file for anything else.
func toUTF16LE(s string) []byte {
	var b bytes.Buffer
	for _, r := range append([]uint16{0xFEFF}, utf16.Encode([]rune(s))...) {
		_ = binary.Write(&b, binary.LittleEndian, r)
	}
	return b.Bytes()
}
