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
	serviceName  = "anywhere-file-agent"
	envPrefix    = "RFM_AGENT_"
)

// servicePlan is the whole of what install and uninstall do: write these files, run these
// commands, and on the way out run those and delete the files again.
type servicePlan struct {
	files     []planFile
	install   []planCmd
	uninstall []planCmd
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
	<key>ProgramArguments</key>
	<array>
		<string>{{x .Exe}}</string>
		<string>run</string>
{{- range .Args}}
		<string>{{x .}}</string>
{{- end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ProcessType</key>
	<string>Background</string>
	<key>StandardOutPath</key>
	<string>{{x .Log}}</string>
	<key>StandardErrorPath</key>
	<string>{{x .Log}}</string>
</dict>
</plist>
`))

func darwinPlan(in planInput) servicePlan {
	path := filepath.Join(in.home, "Library", "LaunchAgents", serviceLabel+".plist")
	body := render(darwinPlist, map[string]any{
		"Label": serviceLabel,
		"Exe":   in.exe,
		"Log":   filepath.Join(in.dir, "agent.log"),
		"Args":  envArgs(in.env),
	})
	return servicePlan{
		files: []planFile{{path: path, body: body}},
		// ponytail: `load -w` rather than `bootstrap gui/$UID`, because it works the same
		// from a login window session and from a headless one. Move to bootstrap if we
		// ever need to name the domain, for example to install for another user.
		install: []planCmd{
			{argv: []string{"launchctl", "unload", "-w", path}, optional: true},
			{argv: []string{"launchctl", "load", "-w", path}},
		},
		uninstall: []planCmd{{argv: []string{"launchctl", "unload", "-w", path}, optional: true}},
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

func linuxPlan(in planInput) servicePlan {
	unit := serviceName + ".service"
	path := filepath.Join(in.home, ".config", "systemd", "user", unit)
	body := render(linuxUnit, map[string]any{"Exe": in.exe, "Args": quoted(envArgs(in.env))})
	return servicePlan{
		files: []planFile{{path: path, body: body}},
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
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>999</Count>
    </RestartOnFailure>
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
	// The scheduler captures nothing a program writes, so on Windows the agent is told
	// where to keep its log. launchd redirects it and systemd has the journal.
	env := slices.Clone(in.env)
	if !hasKey(env, "RFM_AGENT_LOG_FILE") {
		env = append(env, [2]string{"RFM_AGENT_LOG_FILE", filepath.Join(in.dir, "agent.log")})
		sort.Slice(env, func(i, j int) bool { return env[i][0] < env[j][0] })
	}
	args := append([]string{"run"}, quoted(envArgs(env))...)
	return servicePlan{
		files: []planFile{
			{path: task, utf16: true, body: render(windowsTask, map[string]any{
				"User":      in.user,
				"Exe":       in.exe,
				"Arguments": strings.Join(args, " "),
			})},
		},
		install: []planCmd{
			{argv: []string{"schtasks", "/Create", "/TN", serviceName, "/XML", task, "/F"}},
			// The trigger is a logon that has already happened, so the first start is ours.
			{argv: []string{"schtasks", "/Run", "/TN", serviceName}},
		},
		uninstall: []planCmd{
			{argv: []string{"schtasks", "/End", "/TN", serviceName}, optional: true},
			{argv: []string{"schtasks", "/Delete", "/TN", serviceName, "/F"}, optional: true},
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
		output, err := exec.Command(argv[0], argv[1:]...).CombinedOutput()
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
