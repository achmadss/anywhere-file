package main

import (
	"bytes"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// The manifests are the whole of #33: whatever is in them is what the OS does with the
// agent after a reboot, and nobody reads them again until something is wrong. Each one is
// rendered here on every OS, so a Windows mistake fails on the Mac that made it.
func testPlanInput(goos string) planInput {
	return planInput{
		goos: goos,
		exe:  "/opt/anywhere-file/agent",
		dir:  filepath.FromSlash("/home/ana/.config/anywhere-file"),
		home: filepath.FromSlash("/home/ana"),
		user: `WORK\ana`,
		env:  [][2]string{{"RFM_AGENT_ADDR", ":7433"}, {"RFM_AGENT_MDNS", "off"}},
	}
}

func TestEveryManifestStartsTheAgentAndKeepsItRunning(t *testing.T) {
	// What each OS calls "start it again", and what each writes when it will not.
	restarts := map[string]string{
		"darwin":  "<key>KeepAlive</key>",
		"linux":   "Restart=always",
		"windows": "<RestartOnFailure>",
	}
	atLogin := map[string]string{
		"darwin":  "<key>RunAtLoad</key>",
		"linux":   "WantedBy=default.target",
		"windows": "<LogonTrigger>",
	}
	for _, goos := range []string{"darwin", "linux", "windows"} {
		p, err := servicePlanFor(testPlanInput(goos))
		if err != nil {
			t.Fatalf("%s: %v", goos, err)
		}
		all := allBodies(p)
		for what, want := range map[string]string{
			"the agent's path": "/opt/anywhere-file/agent",
			"the run command":  "run",
			"a restart":        restarts[goos],
			"a start at login": atLogin[goos],
			"the environment":  "RFM_AGENT_MDNS",
		} {
			if !strings.Contains(all, want) {
				t.Errorf("%s manifest has no %s (%q)\n%s", goos, what, want, all)
			}
		}
		if len(p.install) == 0 || len(p.uninstall) == 0 {
			t.Errorf("%s: install %v, uninstall %v, want commands for both", goos, p.install, p.uninstall)
		}
	}
}

// A manifest that survives an uninstall is a service that comes back from the dead, so
// everything written has to be named again on the way out.
func TestUninstallRemovesWhatInstallWrote(t *testing.T) {
	for _, goos := range []string{"darwin", "linux", "windows"} {
		p, err := servicePlanFor(testPlanInput(goos))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.files) == 0 {
			t.Fatalf("%s writes no files", goos)
		}
		for _, f := range p.files {
			if !strings.HasPrefix(f.path, filepath.FromSlash("/home/ana")) {
				t.Errorf("%s writes %s, want it under the user's own directories", goos, f.path)
			}
		}
	}
}

// launchd and schtasks read XML. A path with an ampersand in it is a path, and it must not
// end the element it sits in.
func TestAPathWithMarkupInItIsEscaped(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		in := testPlanInput(goos)
		in.exe = filepath.FromSlash("/opt/tom & jerry/agent")
		in.dir = filepath.FromSlash("/opt/tom & jerry")
		p, err := servicePlanFor(in)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range p.files {
			if strings.Contains(f.body, "tom & jerry") {
				t.Errorf("%s manifest has a raw ampersand in it:\n%s", goos, f.body)
			}
			if !strings.Contains(f.body, "tom &amp; jerry") {
				t.Errorf("%s manifest lost the path entirely:\n%s", goos, f.body)
			}
		}
	}
}

// schtasks reads the task definition as Unicode and refuses anything else, which it
// reports as a malformed file rather than as an encoding problem.
func TestTheWindowsTaskIsUTF16(t *testing.T) {
	p, err := servicePlanFor(testPlanInput("windows"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := 0
	for _, task := range p.files {
		if !strings.HasSuffix(task.path, ".xml") {
			continue
		}
		tasks++
		if !task.utf16 {
			t.Fatalf("%s is written as plain bytes, so schtasks will refuse it", task.path)
		}
		encoded := toUTF16LE(task.body)
		if !bytes.HasPrefix(encoded, []byte{0xFF, 0xFE}) {
			t.Errorf("first bytes are % x, want a little endian byte order mark", encoded[:2])
		}
		if got := string(utf16.Decode(decodeUTF16LE(encoded[2:]))); got != task.body {
			t.Errorf("the encoding does not round trip:\n%s", got)
		}
	}
	if tasks == 0 {
		t.Fatal("no task definition in the plan")
	}
}

// A scheduled task has nowhere to put an environment, and a wrapper script that sets one
// becomes the process the scheduler owns, so stopping the task leaves the agent running.
// The settings travel as arguments instead, and the agent is told where to log.
func TestTheWindowsTaskRunsTheAgentItself(t *testing.T) {
	in := testPlanInput("windows")
	in.dir = filepath.FromSlash("/home/ana/Local Settings/anywhere-file")
	p, err := servicePlanFor(in)
	if err != nil {
		t.Fatal(err)
	}
	// The agent's task and the tray's, and nothing to wrap either.
	if len(p.files) != 2 {
		t.Fatalf("files = %v, want the two task definitions and nothing else", p.files)
	}
	body := windowsTaskBody(t, p, serviceName)
	for _, want := range []string{
		"<Command>" + in.exe + "</Command>",
		"RFM_AGENT_MDNS=off",
		// Quoted, because Local Settings is two words to a command line.
		`&#34;RFM_AGENT_LOG_FILE=` + filepath.Join(in.dir, "agent.log") + `&#34;`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the task is missing %s:\n%s", want, body)
		}
	}
}

// The service has no shell to inherit from, so the settings travel in the manifest. What
// else the installing shell holds is none of the service's business.
func TestOnlyTheAgentsOwnSettingsTravelIntoTheService(t *testing.T) {
	env := serviceEnv([]string{
		"RFM_AGENT_MDNS=off",
		"AWS_SECRET_ACCESS_KEY=hunter2",
		"RFM_AGENT_ADDR=:7433",
		"PATH=/usr/bin",
	})
	want := [][2]string{{"RFM_AGENT_ADDR", ":7433"}, {"RFM_AGENT_MDNS", "off"}}
	if len(env) != len(want) {
		t.Fatalf("env = %v, want only the agent's own settings %v", env, want)
	}
	for i, kv := range want {
		if env[i] != kv {
			t.Errorf("env[%d] = %v, want %v (sorted, so the manifest does not churn)", i, env[i], kv)
		}
	}
}

// `go run` is how everything else in the README is run, and a service pointed at its
// output starts once and never again.
func TestATemporaryBuildIsRefused(t *testing.T) {
	tmp := t.TempDir()
	// os.Executable answers with symlinks resolved and os.TempDir does not, which on macOS
	// is the difference between /private/var and /var.
	resolved, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !temporaryBuild(filepath.Join(resolved, "go-build3141", "agent"), tmp) {
		t.Error("a binary under the temporary directory was accepted, want a refusal")
	}
	if temporaryBuild(filepath.Join(t.TempDir()+"-elsewhere", "agent"), tmp) {
		t.Error("an installed binary beside the temporary directory was refused")
	}
}

// The other half of carrying settings as arguments: the agent has to read them back.
func TestSettingsGivenAsArgumentsBecomeTheEnvironment(t *testing.T) {
	t.Setenv("RFM_AGENT_MDNS", "on")
	rest := applyEnvArgs([]string{"RFM_AGENT_MDNS=off", "somethingelse"})
	if got := os.Getenv("RFM_AGENT_MDNS"); got != "off" {
		t.Errorf("RFM_AGENT_MDNS = %q after the argument, want off", got)
	}
	if len(rest) != 1 || rest[0] != "somethingelse" {
		t.Errorf("rest = %v, want everything that is not ours left for the command", rest)
	}
}

func TestAnUnknownOSSaysSoRatherThanWritingNothing(t *testing.T) {
	_, err := servicePlanFor(testPlanInput("plan9"))
	if err == nil {
		t.Fatal("plan9 got a service plan, want an error naming the OS")
	}
	if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("err = %v, want the OS named", err)
	}
}

func allBodies(p servicePlan) string {
	var b strings.Builder
	for _, f := range p.files {
		b.WriteString(f.path + "\n" + f.body + "\n")
	}
	for _, c := range p.install {
		b.WriteString(strings.Join(c.argv, " ") + "\n")
	}
	return b.String()
}

func decodeUTF16LE(b []byte) []uint16 {
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		out = append(out, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return out
}

// The menu bar item (#159) needs the login window's session, and must stay gone after
// Quit, so its job is the one launchd does not keep alive.
func TestTheMacMenuBarItemStartsAtLogonAndIsNotKeptAlive(t *testing.T) {
	p, err := servicePlanFor(testPlanInput("darwin"))
	if err != nil {
		t.Fatal(err)
	}
	var menu string
	for _, f := range p.files {
		if strings.HasSuffix(f.path, menubarLabel+".plist") {
			menu = f.body
		}
		if !strings.Contains(f.body, "<key>AssociatedBundleIdentifiers</key>\n\t<string>io.anywhere-file.agent</string>") {
			t.Errorf("%s is not tied to the app, so Login Items lists it by the binary's name", f.path)
		}
	}
	for _, want := range []string{"<string>menubar</string>", "<string>Aqua</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(menu, want) {
			t.Errorf("menu bar job has no %s:\n%s", want, menu)
		}
	}
	if strings.Contains(menu, "KeepAlive") {
		t.Errorf("menu bar job is kept alive, so Quit would not stick:\n%s", menu)
	}
}

// macOS gives a Background job less CPU and slower disk (#224). The agent serves
// transfers someone is waiting on, so it must not be one.
func TestTheMacAgentJobIsNotThrottledAsBackground(t *testing.T) {
	p, err := servicePlanFor(testPlanInput("darwin"))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.files {
		want := "<key>ProcessType</key>\n\t<string>Standard</string>"
		if strings.HasSuffix(f.path, menubarLabel+".plist") {
			want = "<key>ProcessType</key>\n\t<string>Interactive</string>"
		}
		if !strings.Contains(f.body, want) {
			t.Errorf("%s has no %q:\n%s", f.path, want, f.body)
		}
	}
}

func windowsTaskBody(t *testing.T, p servicePlan, name string) string {
	t.Helper()
	for _, f := range p.files {
		if strings.HasSuffix(f.path, name+".xml") {
			return f.body
		}
	}
	t.Fatalf("no %s task in the plan", name)
	return ""
}

// The tray icon (#160) needs the desktop of whoever logged on, and must stay gone after
// Quit, so its task is the one Windows does not restart.
func TestTheWindowsTrayStartsAtLogonAndIsNotRestarted(t *testing.T) {
	p, err := servicePlanFor(testPlanInput("windows"))
	if err != nil {
		t.Fatal(err)
	}
	tray := windowsTaskBody(t, p, trayName)
	for _, want := range []string{"<Arguments>menubar ", "<LogonTrigger>"} {
		if !strings.Contains(tray, want) {
			t.Errorf("tray task has no %s:\n%s", want, tray)
		}
	}
	if strings.Contains(tray, "RestartOnFailure") {
		t.Errorf("tray task is restarted, so Quit would not stick:\n%s", tray)
	}
	if agent := windowsTaskBody(t, p, serviceName); !strings.Contains(agent, "<RestartOnFailure>") {
		t.Errorf("the agent's own task lost its restart:\n%s", agent)
	}
}

// Quit is undone by opening the app, so it may switch things off but never take away
// what reopening needs.
func TestQuitSwitchesOffAndReopenSwitchesBackOn(t *testing.T) {
	for _, goos := range []string{"darwin", "windows"} {
		p, err := servicePlanFor(testPlanInput(goos))
		if err != nil {
			t.Fatal(err)
		}
		if len(p.quit) == 0 || len(p.reopen) == 0 {
			t.Errorf("%s: quit %v, reopen %v, want commands for both", goos, p.quit, p.reopen)
		}
		for _, c := range p.quit {
			for _, a := range c.argv {
				if a == "/Delete" || a == "rm" || a == "remove" {
					t.Errorf("%s quit deletes something: %v", goos, c.argv)
				}
			}
		}
	}
}

// #223: an app dragged to the Trash leaves the jobs behind. Each job runs its own
// ProgramArguments here, with launchctl swapped for a stub, to check that a missing binary
// deletes the plist and the job, and that a present one is what runs.
func TestAMacJobWhoseBinaryIsGoneRemovesItself(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs /bin/sh")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "launchctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$@\" >>\"$0.log\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")

	in := testPlanInput("darwin")
	in.home = dir
	in.exe = filepath.Join(dir, "agent")
	p, err := servicePlanFor(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Library", "LaunchAgents"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range p.files {
		if err := os.WriteFile(f.path, []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
		args := programArguments(t, f.body)
		if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
			t.Fatalf("%s: %v", f.path, err)
		}
		if _, err := os.Stat(f.path); !os.IsNotExist(err) {
			t.Errorf("%s is still there with no binary, so the next login loads it again", f.path)
		}
	}
	log, _ := os.ReadFile(stub + ".log")
	for _, label := range []string{serviceLabel, menubarLabel} {
		if !strings.Contains(string(log), "remove "+label+"\n") {
			t.Errorf("launchctl was not asked to remove %s, it got:\n%s", label, log)
		}
	}

	// With the binary there, the job is the binary.
	if err := os.WriteFile(in.exe, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	args := programArguments(t, p.files[0].body)
	out, err := exec.Command(args[0], args[1:]...).Output()
	if err != nil || !strings.HasPrefix(string(out), "run ") {
		t.Errorf("the job ran %q (%v), want the agent's run command", out, err)
	}
}

func programArguments(t *testing.T, plist string) []string {
	t.Helper()
	_, array, _ := strings.Cut(plist, "<key>ProgramArguments</key>")
	array, _, _ = strings.Cut(array, "</array>")
	var args []string
	for _, s := range strings.Split(array, "<string>")[1:] {
		s, _, _ = strings.Cut(s, "</string>")
		args = append(args, html.UnescapeString(s))
	}
	if len(args) == 0 {
		t.Fatalf("no ProgramArguments in:\n%s", plist)
	}
	return args
}

// Task names are one list for the whole PC, so a second user's would replace the first
// user's, or be refused, without the user's name in them (#222).
func TestEachWindowsUserHasTheirOwnTasks(t *testing.T) {
	p, err := servicePlanFor(testPlanInput("windows"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range append(p.install, p.reopen...) {
		if i := slices.Index(c.argv, "/TN"); i >= 0 && !strings.HasSuffix(c.argv[i+1], "-ana") {
			t.Errorf("%q names a task every user shares", c.argv)
		}
	}
}
