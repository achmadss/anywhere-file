package main

import (
	"net/http"
	"strings"
	"testing"
)

// No database: the website pages are templates and a route table.
func newWebMux() *http.ServeMux {
	mux := http.NewServeMux()
	registerWebRoutes(mux, nil)
	return mux
}

// #179: the landing page renders and carries the shared header and footer.
func TestTheLandingPageRenders(t *testing.T) {
	rec := getPage(t, newWebMux(), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want HTML", ct)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Reach the files on your home PCs from your phone or laptop.",
		"On your Wi-Fi",
		"Over the internet",
		"No router setup",
		"one-time invite link",
		"Windows, macOS, Linux",
		"Android, Windows, macOS, Linux",
		"ProductName",
		`<a class="logo" href="/">`,
		`<a class="nav-link outline" href="/signin">Sign in</a>`,
		`href="/download"`,
		`href="/privacy"`,
		`href="/terms"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/ lacks %q", want)
		}
	}
}

// The claims the page must not make, because they are false today.
func TestTheLandingPageMakesNoFalseClaims(t *testing.T) {
	body := strings.ToLower(getPage(t, newWebMux(), "/").Body.String())
	for _, banned := range []string{"end-to-end", "iphone", "ios", "sync", "backup", "cloud", "signed"} {
		if strings.Contains(body, banned) {
			t.Errorf("/ claims %q", banned)
		}
	}
}

// #179: the header offers Pricing once billing is built, so the landing page renders
// no Pricing link yet.
func TestTheLandingPageHasNoPricingYet(t *testing.T) {
	body := getPage(t, newWebMux(), "/").Body.String()
	if strings.Contains(body, "Pricing") {
		t.Error("/ renders a Pricing link before billing is built")
	}
}

// #182: the legal pages render, each with its placeholder body.
func TestTheLegalPagesRender(t *testing.T) {
	for path, want := range map[string]string{
		"/privacy": "The privacy policy text is being written.",
		"/terms":   "The terms text is being written.",
	} {
		rec := getPage(t, newWebMux(), path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s lacks the placeholder body %q", path, want)
		}
	}
}

// #182: the signup page links both legal pages, through the shared footer.
func TestTheSignupPageLinksTheLegalPages(t *testing.T) {
	body := getPage(t, newWebMux(), "/signup").Body.String()
	for _, want := range []string{`href="/privacy"`, `href="/terms"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/signup lacks %q", want)
		}
	}
}

// #253: the sign-in page renders, posts to the existing sign-in endpoint, and links
// the signup page back.
func TestTheSigninPageRenders(t *testing.T) {
	rec := getPage(t, newWebMux(), "/signin")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	// html/template escapes the slash inside the script that carries the form action.
	body := strings.ReplaceAll(rec.Body.String(), `\/`, "/")
	for _, want := range []string{
		"Sign in",
		"/v1/auth/signin",
		`name="email"`, `name="password"`,
		"current-password",
		`<a href="/signup">Create an account</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/signin lacks %q", want)
		}
	}
}

// #253: the signup page links back to sign in.
func TestTheSignupPageLinksToSignin(t *testing.T) {
	if body := getPage(t, newWebMux(), "/signup").Body.String(); !strings.Contains(body, `<a href="/signin">Sign in</a>`) {
		t.Error("/signup lacks a link to /signin")
	}
}
