package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The download page (#138). The packages live on a GitHub release, made by the release
// workflow when a version tag is pushed. This page asks GitHub which release is current,
// remembers the answer for a while, and puts the file for the visitor's operating system
// first. Nothing is published from here: the page points at what the workflow attached.

// releasesAPI is the GitHub endpoint that describes the latest release. A test points it
// at a fake.
var releasesAPI = "https://api.github.com/repos/achmadss/anywhere-file/releases/latest"

// releasesPage is where a visitor is sent when GitHub cannot be asked.
const releasesPage = "https://github.com/achmadss/anywhere-file/releases"

// releaseCacheFor is how long one answer from GitHub is reused. Unauthenticated calls are
// limited to sixty an hour per address, and a release changes less often than that.
const releaseCacheFor = 10 * time.Minute

type download struct {
	Name string
	URL  string
}

// system is one operating system's part of the page: its files, the warning an unsigned
// package draws, and how to reach the agent's settings once it is installed.
type system struct {
	Name    string
	Files   []download
	Warning string
	Then    string
}

type release struct {
	Version string
	Systems []system
	Page    string // the release on GitHub, for whatever is not listed
}

// systems is what the page says about each operating system. The order is what a visitor
// whose system is not recognised sees.
var systems = []system{
	{
		Name: "macOS",
		Warning: "The package is not signed yet, so macOS says it is from an unidentified " +
			"developer. Open it, let the warning appear, then go to System Settings, " +
			"Privacy and Security, scroll to Security and press Open Anyway. The same panel " +
			"is where the agent's request to use the local network is granted, under Local Network.",
		Then: "The agent starts on its own. Open its settings page from a terminal with " +
			"/Applications/anywhere-file.app/Contents/MacOS/agent settings.",
	},
	{
		Name: "Windows",
		Warning: "The installer is not signed yet, so SmartScreen warns about an unknown " +
			"publisher. Press More info, then Run anyway.",
		Then: "The agent starts on its own. Open its settings page from a terminal with " +
			"\"C:\\Program Files\\anywhere-file\\agent.exe\" settings.",
	},
	{
		Name:    "Linux",
		Warning: "Nothing checks a signature on Linux, so nothing warns.",
		Then: "The deb starts the agent at logon. The tarball installs under your home with " +
			"its install.sh. Either way, anywhere-file settings is in the applications menu " +
			"and opens the settings page, and anywhere-file-agent settings does the same from a terminal.",
	},
}

// releaseCache is the last answer from GitHub and when it was fetched. The mutex is held
// across the fetch, so a burst of visitors while the cache is stale makes one request.
type releaseCache struct {
	mu      sync.Mutex
	fetched time.Time
	last    *release
	client  *http.Client
}

var latestRelease = &releaseCache{client: &http.Client{Timeout: 5 * time.Second}}

// get returns the current release, or nil when GitHub has not answered yet and cannot
// be reached now. A stale answer is kept over no answer.
func (c *releaseCache) get(ctx context.Context) *release {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.last != nil && time.Since(c.fetched) < releaseCacheFor {
		return c.last
	}
	rel, err := c.fetch(ctx)
	if err != nil {
		return c.last
	}
	c.last, c.fetched = rel, time.Now()
	return rel
}

func (c *releaseCache) fetch(ctx context.Context) (*release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github answered %d", resp.StatusCode)
	}
	var out struct {
		Tag    string `json:"tag_name"`
		URL    string `json:"html_url"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, err
	}
	rel := &release{Version: strings.TrimPrefix(out.Tag, "v"), Page: out.URL}
	for _, s := range systems {
		s.Files = nil
		for _, a := range out.Assets {
			if systemOf(a.Name) == s.Name {
				s.Files = append(s.Files, download{Name: a.Name, URL: a.URL})
			}
		}
		rel.Systems = append(rel.Systems, s)
	}
	return rel, nil
}

// systemOf says which operating system a package file is for, from the name the build
// scripts in packaging/ give it.
func systemOf(name string) string {
	switch {
	case strings.HasSuffix(name, ".pkg"):
		return "macOS"
	case strings.HasSuffix(name, ".msi"):
		return "Windows"
	case strings.HasSuffix(name, ".deb"), strings.Contains(name, "-linux-"):
		return "Linux"
	}
	return ""
}

// systemFor names the visitor's operating system from the User-Agent, or "" when it is
// none of the three. Android says Linux, and the Linux packages are the nearest thing.
func systemFor(userAgent string) string {
	switch {
	case strings.Contains(userAgent, "Windows"):
		return "Windows"
	case strings.Contains(userAgent, "Mac OS X"), strings.Contains(userAgent, "Macintosh"):
		return "macOS"
	case strings.Contains(userAgent, "Linux"), strings.Contains(userAgent, "X11"):
		return "Linux"
	}
	return ""
}

// downloadPage renders the release with the visitor's system first. With no release to
// show, it sends the visitor to the releases page instead of a page with nothing on it.
func downloadPage(w http.ResponseWriter, r *http.Request) {
	rel := latestRelease.get(r.Context())
	if rel == nil {
		renderTemplate(w, "download", release{Page: releasesPage})
		return
	}
	mine := systemFor(r.UserAgent())
	var ordered []system
	for _, s := range rel.Systems {
		if s.Name == mine {
			ordered = append([]system{s}, ordered...)
		} else {
			ordered = append(ordered, s)
		}
	}
	renderTemplate(w, "download", release{Version: rel.Version, Systems: ordered, Page: rel.Page})
}
