package main

// `agent share` and `agent settings` (#136). The same endpoint the page uses, so a PC with
// no screen is set up over ssh and ends in exactly the state a browser would have left it
// in. With the agent stopped there is nothing holding the registry, so the command writes
// the file itself.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"
)

// settingsDial is how long the command waits to find out whether the agent is up. It is a
// connection to this machine, so anything longer than this is not a slow network.
const settingsDial = 500 * time.Millisecond

func shareCommand(ctx context.Context, cfg config, out io.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("share needs one of: add, list, rm")
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "list":
		apps, err := currentShares(ctx, cfg)
		if err != nil {
			return err
		}
		for _, s := range apps {
			fmt.Fprintf(out, "%s\t%s\t%s\n", s.Name, s.Address, s.Path)
		}
		fmt.Fprintf(out, "%d shared\n", len(apps))
		return nil

	case "add":
		fs := flag.NewFlagSet("share add", flag.ContinueOnError)
		fs.SetOutput(out)
		name := fs.String("name", "", "what to call it, taken from the directory when left out")
		if err := fs.Parse(flagsFirst(args)); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("share add needs one directory")
		}
		s, err := addShareTo(ctx, cfg, fs.Arg(0), *name)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "sharing %s as %s\n", s.Path, s.Name)
		return nil

	case "rm":
		if len(args) != 1 {
			return fmt.Errorf("share rm needs one name")
		}
		if err := removeShareFrom(ctx, cfg, args[0]); err != nil {
			return err
		}
		fmt.Fprintf(out, "stopped sharing %s\n", args[0])
		return nil
	}
	return fmt.Errorf("unknown share command %q, want add, list or rm", sub)
}

// flagsFirst moves the options in front of the directory. `share add ~/Shared --name files`
// is the order everyone types, and the flag package stops reading at the first argument
// that is not an option. Every option here takes a value, so one written apart from its
// value brings the next argument with it.
func flagsFirst(args []string) []string {
	var flags, rest []string
	for i := 0; i < len(args); i++ {
		if !strings.HasPrefix(args[i], "-") {
			rest = append(rest, args[i])
			continue
		}
		flags = append(flags, args[i])
		if !strings.Contains(args[i], "=") && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, rest...)
}

// settingsCommand opens the page. It is what the menu bar, the tray and the applications
// menu run, so the token never has to be copied anywhere by hand.
func settingsCommand(ctx context.Context, cfg config, out io.Writer) error {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return err
	}
	if c == nil {
		return fmt.Errorf("the agent is not running on this PC, so there is no settings page. Start it, or use `agent share add`")
	}
	url := c.base + "/?t=" + c.token
	fmt.Fprintln(out, url)
	return openBrowser(url)
}

// settingsClient talks to the running agent. A nil one means nothing is listening, which
// is the signal to write the registry directly.
type settingsClient struct {
	base  string
	token string
	hc    *http.Client
}

// dialSettings reports whether the agent is up, and returns what to talk to it with. The
// address is on this machine, so a connection either happens at once or is not going to.
func dialSettings(ctx context.Context, cfg config) (*settingsClient, error) {
	if cfg.settings == "" {
		return nil, nil
	}
	conn, err := (&net.Dialer{Timeout: settingsDial}).DialContext(ctx, "tcp", cfg.settings)
	if err != nil {
		return nil, nil
	}
	_ = conn.Close()
	token, err := settingsToken(cfg.dir)
	if err != nil {
		return nil, err
	}
	return &settingsClient{
		base:  "http://" + cfg.settings,
		token: token,
		hc:    &http.Client{Timeout: enrolTimeout},
	}, nil
}

func (c *settingsClient) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return serverAnswerError(resp.StatusCode, answer)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(answer, out)
}

func currentShares(ctx context.Context, cfg config) ([]share, error) {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if c == nil {
		st, err := loadState(cfg.dir)
		if err != nil {
			return nil, err
		}
		return shares(st.Apps), nil
	}
	var out struct {
		Apps []share `json:"apps"`
	}
	err = c.do(ctx, http.MethodGet, "/v1/apps", nil, &out)
	return out.Apps, err
}

func addShareTo(ctx context.Context, cfg config, dir, name string) (share, error) {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return share{}, err
	}
	if c == nil {
		return addShareOffline(cfg, dir, name)
	}
	var out share
	err = c.do(ctx, http.MethodPost, "/v1/apps", map[string]string{"path": dir, "name": name}, &out)
	return out, err
}

func removeShareFrom(ctx context.Context, cfg config, name string) error {
	c, err := dialSettings(ctx, cfg)
	if err != nil {
		return err
	}
	if c == nil {
		return removeShareOffline(cfg, name)
	}
	return c.do(ctx, http.MethodDelete, "/v1/apps/"+name, nil, nil)
}

func addShareOffline(cfg config, dir, name string) (share, error) {
	st, err := loadState(cfg.dir)
	if err != nil {
		return share{}, err
	}
	a, err := newShare(dir, name, st.Apps)
	if err != nil {
		return share{}, err
	}
	st.Apps = append(st.Apps, a)
	if err := st.validate(); err != nil {
		return share{}, err
	}
	if err := saveState(cfg.dir, st); err != nil {
		return share{}, err
	}
	return share{Name: a.Name, Address: a.Address, Path: sharedPath(a)}, nil
}

func removeShareOffline(cfg config, name string) error {
	st, err := loadState(cfg.dir)
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(slices.Clone(st.Apps), func(a app) bool { return a.Name == name })
	if len(kept) == len(st.Apps) {
		return fmt.Errorf("this PC does not share %s", name)
	}
	st.Apps = kept
	return saveState(cfg.dir, st)
}

// openBrowser hands a URL to whatever opens one here. The settings UI is a web page so
// that the agent carries no window toolkit, and this is the whole of what that costs.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		// Through the shell handler rather than a browser, because `start` is a built-in
		// of cmd and nothing else knows which browser this user has.
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("nothing here opens a browser, so open the address above yourself: %w", err)
	}
	// Released rather than waited for: xdg-open can hand over to a browser that then runs
	// for the rest of the day.
	return cmd.Process.Release()
}
