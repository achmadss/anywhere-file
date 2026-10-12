package main

import (
	"context"
	"embed"
	"html/template"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The account pages. #100 opens a browser to sign up instead of putting a form in the
// client, and the verification and reset messages need somewhere for their links to land.
// This process serves them with html/template and embed, so there is nothing else to
// deploy and no build step.

//go:embed web.html download.html
var webFiles embed.FS

var webTemplate = template.Must(template.ParseFS(webFiles, "web.html", "download.html"))

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

// link is one way out of a page other than its form. On /approve the sign-in form offers
// to create an account or reset the password first, both carrying the approval code, so
// the person arrives back here with it (#186).
type link struct {
	Href string
	Text string
}

// page is one of the account pages. The browser posts Action with every field on it, plus
// the token from the query string when the link carried one.
type page struct {
	Title  string
	Intro  string
	Fields []field
	Action string
	Method string
	Submit string
	Danger bool   // the button reads in the error colour, for the one action that destroys
	Deny   string // a second button, which posts approve=false
	Done   string
	OnLoad bool // post as soon as the page opens, with no button to press
	Reload bool // on success, load the page again rather than say Done
	Links  []link

	// Back names the pages a person can arrive at with an approval code they still have
	// to answer follow-up mail for, like a confirmation or a reset link (#186). When such
	// a link carries the code again and a live enrolment answers to it, the page is
	// rendered with the way back: the last page of the signup or reset flow offers
	// "Back to approving", so a PC stays waiting instead of being orphaned in the middle.
	Back   bool
	BackTo string // where the way back goes, when there is one
	BackPC string // the name of the PC the approval is about, in the link's text
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
		Back:   true,
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
		Back:   true,
	},
}

func registerWebRoutes(mux *http.ServeMux, db *pgxpool.Pool) {
	for path, p := range pages {
		mux.Handle("GET "+path, servePage(db, p))
	}
	// The approval page tells a visitor whether a code is live, which is the one thing
	// worth guessing here, so it is rate limited the way the endpoints are.
	mux.Handle("GET /approve", newRateLimiter(approvePageLimit, rateWindow).middleware(approvePage(db)))
	// Deleting an account goes through the page rather than the app (#183), so it says
	// plainly what the deletion takes away before the person commits to it. Like every
	// other page it reveals nothing to a signed-out visitor, so it carries no limiter.
	mux.Handle("GET /account/delete", accountDeletePage(db))
	mux.HandleFunc("GET /download", downloadPage)
	mux.HandleFunc("GET /v1/version", latestVersion)
}

// signInPage is the form a signed-out visitor gets. After a sign-in the page loads again,
// now signed in, which is how /approve comes back with its code in the URL and /account
// delete comes back to the form that does the deletion.
func signInPage(title, intro string) page {
	return page{
		Title:  title,
		Intro:  intro,
		Fields: []field{{"email", "Email", "email", "username"}, {"password", "Password", "password", "current-password"}},
		Action: "/v1/auth/signin",
		Submit: "Sign in",
		Reload: true,
	}
}

// approvePage is the only page that reads the database before it renders. It has to name
// the PC that is asking and know whether anyone is signed in, and both change what it says.
//
// Signing in happens on the page itself rather than somewhere else the visitor has to come
// back from: the code is already in this URL, and reloading brings it back with the cookie.
// An account that does not exist yet is made from here too, through offers that carry the
// code onward (#186).
func approvePage(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a, _, err := lookupSession(r.Context(), db, sessionTokenFromRequest(r))
		if err != nil {
			p := signInPage("Sign in to approve",
				"A PC is asking to join an account. Sign in to the account it should join.")
			if code := r.URL.Query().Get("code"); code != "" {
				p.Links = []link{
					{Href: "/signup?code=" + url.QueryEscape(code), Text: "Create an account"},
					{Href: "/reset?code=" + url.QueryEscape(code), Text: "Forgot your password"},
				}
			}
			render(w, p)
			return
		}
		name, ok := pendingPCName(r.Context(), db, r.URL.Query().Get("code"))
		if !ok {
			render(w, page{
				Title: "Nothing to approve",
				Intro: "That request expired or was already answered. Ask the PC to sign in again.",
			})
			return
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

// pendingPCName resolves an approval code to the name of the PC still asking for it.
// Unknown, used, refused and expired codes all answer not ok: a page can only tell that
// there is nothing waiting, never which of those it is.
func pendingPCName(ctx context.Context, db *pgxpool.Pool, code string) (string, bool) {
	var name string
	err := db.QueryRow(ctx,
		`SELECT name FROM device_enrolments
		 WHERE code_hash = $1 AND status = 'pending' AND used_at IS NULL AND expires_at > now()`,
		hashToken(normalizeUserCode(code))).Scan(&name)
	if err != nil {
		return "", false
	}
	if name == "" {
		name = "A PC that gave no name"
	}
	return name, true
}

// accountDeletePage is the one page of account management the website carries (#183), so
// deleting does not need the app. Signed out it shows the sign-in form, and the reload
// after a sign-in lands the person on the form below. The form posts DELETE to the same
// endpoint the app would call, so the page adds no endpoint of its own.
func accountDeletePage(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := lookupSession(r.Context(), db, sessionTokenFromRequest(r)); err != nil {
			render(w, signInPage("Sign in to delete your account",
				"Sign in to the account you want to delete."))
			return
		}
		render(w, page{
			Title: "Delete your account",
			Intro: "You lose access to every PC on the account. The PCs keep sharing on their own " +
				"network, and other people keep their access. A PC where you were the only admin has " +
				"nobody left to manage it, so sign it out from its settings page first if it should " +
				"join another account.",
			Fields: []field{{"password", "Password", "password", "current-password"}},
			Action: "/v1/account",
			Method: http.MethodDelete,
			Submit: "Delete my account",
			Danger: true,
			Done:   "Your account is deleted.",
		})
	}
}

// servePage renders one page. When the page is one someone can reach with an approval code
// still outstanding, a live code in the query string earns the way back to approving it.
// The backtrack pages are the only ones that read the database, and only when a code came
// along to look up.
func servePage(db *pgxpool.Pool, p page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := p // one copy per request; the handler is shared by every visitor
		if code := r.URL.Query().Get("code"); q.Back && db != nil && code != "" {
			if name, ok := pendingPCName(r.Context(), db, code); ok {
				q.BackTo = "/approve?code=" + url.QueryEscape(code)
				q.BackPC = name
			}
		}
		render(w, q)
	}
}

// render writes one page. The reset, verification and approval links carry a secret in the
// query string, so the headers keep it out of a Referer and off any other origin.
func render(w http.ResponseWriter, p page) { renderTemplate(w, "web.html", p) }

// renderTemplate writes one page from the named template, with the headers every page
// carries.
func renderTemplate(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "+
			"connect-src 'self'; form-action 'none'; base-uri 'none'; frame-ancestors 'none'")
	if err := webTemplate.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, "try again later", http.StatusInternalServerError)
	}
}

// approvalCode appends the approval code a page carried to a link that opens another page
// of the same flow, so that page can offer the way back (#186). A code nobody asked about
// does nothing: the page behind the link looks it up and links back only when a live
// enrolment answers to it.
func approvalCode(code string) string {
	if code == "" {
		return ""
	}
	return "&code=" + url.QueryEscape(code)
}
