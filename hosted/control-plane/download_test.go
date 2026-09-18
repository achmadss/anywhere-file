package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub answers the one call the download page makes, with a release carrying a file
// for each operating system.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v0.1.0","html_url":"https://example.test/releases/v0.1.0","assets":[
			{"name":"anywhere-file-0.1.0-linux-amd64.tar.gz","browser_download_url":"https://example.test/linux-amd64.tar.gz"},
			{"name":"anywhere-file_0.1.0_amd64.deb","browser_download_url":"https://example.test/amd64.deb"},
			{"name":"anywhere-file-0.1.0.pkg","browser_download_url":"https://example.test/mac.pkg"},
			{"name":"anywhere-file-0.1.0.msi","browser_download_url":"https://example.test/win.msi"}]}`))
	}))
	t.Cleanup(srv.Close)
	old := releasesAPI
	releasesAPI = srv.URL
	latestRelease = &releaseCache{client: srv.Client()}
	t.Cleanup(func() { releasesAPI = old; latestRelease = &releaseCache{client: &http.Client{}} })
	return srv
}

func downloadAs(t *testing.T, userAgent string) string {
	t.Helper()
	mux := http.NewServeMux()
	registerWebRoutes(mux, nil)
	req := httptest.NewRequest(http.MethodGet, "/download", nil)
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// #138's acceptance: the page offers the right file to a visitor on each of the three
// systems, and says what the warning looks like on the two that show one.
func TestTheDownloadPageOffersTheVisitorsSystemFirst(t *testing.T) {
	fakeGitHub(t)
	for ua, want := range map[string]string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64)":    "https://example.test/win.msi",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5)": "https://example.test/mac.pkg",
		"Mozilla/5.0 (X11; Linux x86_64)":              "https://example.test/linux-amd64.tar.gz",
		"Mozilla/5.0 (Linux; Android 14)":              "https://example.test/linux-amd64.tar.gz",
		"curl/8.0":                                     "https://example.test/mac.pkg",
	} {
		body := downloadAs(t, ua)
		first := body[strings.Index(body, `<a href="`)+len(`<a href="`):]
		first = first[:strings.Index(first, `"`)]
		if first != want {
			t.Errorf("%s: first link is %s, want %s", ua, first, want)
		}
		for _, text := range []string{"Download anywhere-file 0.1.0", "Open Anyway", "Run anyway", "https://example.test/amd64.deb"} {
			if !strings.Contains(body, text) {
				t.Errorf("%s: page lacks %q", ua, text)
			}
		}
	}
}

// GitHub is asked once and the answer kept, so a visitor never waits on it and an outage
// after the first answer changes nothing.
func TestTheDownloadPageKeepsTheLastAnswer(t *testing.T) {
	srv := fakeGitHub(t)
	downloadAs(t, "curl/8.0")
	srv.Close()
	if body := downloadAs(t, "curl/8.0"); !strings.Contains(body, "https://example.test/mac.pkg") {
		t.Error("the page forgot the release once GitHub went away")
	}
}

// With nothing remembered and GitHub unreachable, the visitor is sent to the releases page
// rather than shown an empty list.
func TestTheDownloadPageFallsBackToTheReleasesPage(t *testing.T) {
	srv := fakeGitHub(t)
	srv.Close()
	if body := downloadAs(t, "curl/8.0"); !strings.Contains(body, releasesPage) || strings.Contains(body, "example.test") {
		t.Errorf("page = %s", body)
	}
}
