package main

import (
	"embed"
	"html/template"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The account pages. #100 opens a browser to sign up instead of putting a form in the
// client, and the verification and reset messages need somewhere for their links to land.
// This process serves them with html/template and embed, so there is nothing else to
// deploy and no build step.

//go:embed web.html
var webFiles embed.FS

var webTemplate = template.Must(template.ParseFS(webFiles, "web.html"))

// field is one input. Its name is the JSON key the endpoint under /v1 already reads, so a
// page posts the body the client posts and nothing under /v1 changes shape. Auto is what
// the browser's password manager reads, and it differs between signing in and signing up
// on fields that otherwise look the same.
type field struct {
	Name  string
	Label string
	Type  string
	Auto  string
}

// page is one of the account pages. The browser posts Action with every field on it, plus
// the token from the query string when the link carried one.
type page struct {
	Title  string
	Intro  string
	Fields []field
	Action string
	Submit string
	Deny   string // a second button, which posts approve=false
	Done   string
	OnLoad bool // post as soon as the page opens, with no button to press
	Reload bool // on success, load the page again rather than say Done
}

var pages = map[string]page{
	"/signup": {
		Title:  "Create an account",
		Intro:  "One account reaches every PC you set up.",
		Fields: []field{{"email", "Email", "email", "username"}, {"password", "Password", "password", "new-password"}},
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
		Fields: []field{{"email", "Email", "email", "username"}},
		Action: "/v1/auth/password-reset/request",
		Submit: "Send the link",
		Done:   "If there is an account for that address, a link is on its way to it.",
	},
	"/reset/confirm": {
		Title:  "Choose a new password",
		Intro:  "At least 12 characters.",
		Fields: []field{{"new_password", "New password", "password", "new-password"}},
		Action: "/v1/auth/password-reset/confirm",
		Submit: "Save it",
		Done:   "The password is changed and every session that was open is signed out. Sign in from the app.",
	},
}

func registerWebRoutes(mux *http.ServeMux, db *pgxpool.Pool) {
	for path, p := range pages {
		mux.Handle("GET "+path, servePage(p))
	}
	// The approval page tells a visitor whether a code is live, which is the one thing
	// worth guessing here, so it is rate limited the way the endpoints are.
	mux.Handle("GET /approve", newRateLimiter(approvePageLimit, rateWindow).middleware(approvePage(db)))
}

// approvePage is the only page that reads the database before it renders. It has to name
// the PC that is asking and know whether anyone is signed in, and both change what it says.
//
// Signing in happens on the page itself rather than somewhere else the visitor has to come
// back from: the code is already in this URL, and reloading brings it back with the cookie.
func approvePage(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, err := lookupSession(r.Context(), db, sessionTokenFromRequest(r))
		if err != nil {
			render(w, page{
				Title:  "Sign in to approve",
				Intro:  "A PC is asking to join an account. Sign in to the account it should join.",
				Fields: []field{{"email", "Email", "email", "username"}, {"password", "Password", "password", "current-password"}},
				Action: "/v1/auth/signin",
				Submit: "Sign in",
				Reload: true,
			})
			return
		}
		var name string
		err = db.QueryRow(r.Context(),
			`SELECT name FROM device_enrolments
			 WHERE code_hash = $1 AND status = 'pending' AND used_at IS NULL AND expires_at > now()`,
			hashToken(normalizeUserCode(r.URL.Query().Get("code")))).Scan(&name)
		if err != nil {
			render(w, page{
				Title: "Nothing to approve",
				Intro: "That request expired or was already answered. Ask the PC to sign in again.",
			})
			return
		}
		if name == "" {
			name = "A PC that gave no name"
		}
		render(w, page{
			Title: "Approve this PC?",
			Intro: name + " is asking to join " + a.email + ". Approving lets you reach it from " +
				"away. Only approve it if you just asked this PC to sign in.",
			Action: "/v1/devices/enrolment/answer",
			Submit: "Approve",
			Deny:   "Refuse",
		})
	}
}

// servePage renders one page. The reset and verification links carry a token in the query
// string, so the headers keep it out of a Referer and off any other origin.
func servePage(p page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { render(w, p) }
}

// render writes one page. The reset, verification and approval links carry a secret in the
// query string, so the headers keep it out of a Referer and off any other origin.
func render(w http.ResponseWriter, p page) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	if err := webTemplate.Execute(w, p); err != nil {
		http.Error(w, "try again later", http.StatusInternalServerError)
	}
}
