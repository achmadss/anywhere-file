package main

import (
	"embed"
	"html/template"
	"net/http"
)

// The account pages. #100 opens a browser to sign up instead of putting a form in the
// client, and the verification and reset messages need somewhere for their links to land.
// This process serves them with html/template and embed, so there is nothing else to
// deploy and no build step.

//go:embed web.html
var webFiles embed.FS

var webTemplate = template.Must(template.ParseFS(webFiles, "web.html"))

// field is one input. Its name is the JSON key the endpoint under /v1 already reads, so a
// page posts the body the client posts and nothing under /v1 changes shape.
type field struct {
	Name  string
	Label string
	Type  string
}

// page is one of the account pages. The browser posts Action with every field on it, plus
// the token from the query string when the link carried one.
type page struct {
	Title  string
	Intro  string
	Fields []field
	Action string
	Submit string
	Done   string
	OnLoad bool // post as soon as the page opens, with no button to press
}

var pages = map[string]page{
	"/signup": {
		Title:  "Create an account",
		Intro:  "One account reaches every PC you set up.",
		Fields: []field{{"email", "Email", "email"}, {"password", "Password", "password"}},
		Action: "/v1/auth/signup",
		Submit: "Sign up",
		Done:   "Check your email for a link that confirms the address, then sign in from the app.",
	},
	"/verify": {
		Title:  "Confirm your address",
		Action: "/v1/auth/verify",
		OnLoad: true,
		Done:   "The address is confirmed. Sign in from the app.",
	},
	"/reset": {
		Title:  "Reset your password",
		Intro:  "A link goes to the address on the account.",
		Fields: []field{{"email", "Email", "email"}},
		Action: "/v1/auth/password-reset/request",
		Submit: "Send the link",
		Done:   "If there is an account for that address, a link is on its way to it.",
	},
	"/reset/confirm": {
		Title:  "Choose a new password",
		Intro:  "At least 12 characters.",
		Fields: []field{{"new_password", "New password", "password"}},
		Action: "/v1/auth/password-reset/confirm",
		Submit: "Save it",
		Done:   "The password is changed and every session that was open is signed out. Sign in from the app.",
	},
}

func registerWebRoutes(mux *http.ServeMux) {
	for path, p := range pages {
		mux.Handle("GET "+path, servePage(p))
	}
}

// servePage renders one page. The reset and verification links carry a token in the query
// string, so the headers keep it out of a Referer and off any other origin.
func servePage(p page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
				"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
		if err := webTemplate.Execute(w, p); err != nil {
			http.Error(w, "try again later", http.StatusInternalServerError)
		}
	}
}
