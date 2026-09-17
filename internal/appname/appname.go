// Package appname holds the rule for what an application may be called.
//
// A name reaches a URL on both sides: the control plane routes `/d/{device}/{app}` by it
// and the agent's gateway resolves it to a loopback address. The rule lives here so the
// agent cannot register a name the server will refuse, and so neither side can drift into
// accepting a name that carries a path, a query or an escape.
package appname

import "regexp"

// MaxLen is the longest name or type, in bytes.
const MaxLen = 32

var pattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// Valid reports whether s is an acceptable application name or type: lowercase letters,
// digits and hyphens, starting with a letter or a digit.
func Valid(s string) bool {
	return len(s) <= MaxLen && pattern.MatchString(s)
}
