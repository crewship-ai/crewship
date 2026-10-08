// Package slug holds the one format rule for user-chosen slugs (workspaces,
// crews, agents), so the API and the backup restore judge a name the same way.
package slug

import "regexp"

var validRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Valid reports whether s is a safe slug: lowercase letters, digits, "-" and
// "_", starting with a letter or digit.
func Valid(s string) bool {
	return validRe.MatchString(s)
}
