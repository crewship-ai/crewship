package api

import (
	"regexp"
	"strings"
	"testing"
)

// BenchmarkAuthSlugify_Inline mirrors the pre-fix Signup / Bootstrap code
// path: compile the regex on every call. This is the shape the hot path
// used before auth.go was updated to share a package-level var.
func BenchmarkAuthSlugify_Inline(b *testing.B) {
	emails := []string{
		"alice.example@example.com",
		"bob+tag@example.io",
		"Some.Weird-Address@ACME.corp",
		"developer.crewship@anthropic.com",
		"plain@local",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		email := emails[i%len(emails)]
		slugBase := strings.Split(email, "@")[0]
		_ = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(strings.ToLower(slugBase), "-")
	}
}

// BenchmarkAuthSlugify_Hoisted uses the package-level emailSlugCleanRE that
// replaces the inline compile. Exercises the same input set.
func BenchmarkAuthSlugify_Hoisted(b *testing.B) {
	emails := []string{
		"alice.example@example.com",
		"bob+tag@example.io",
		"Some.Weird-Address@ACME.corp",
		"developer.crewship@anthropic.com",
		"plain@local",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		email := emails[i%len(emails)]
		slugBase := strings.Split(email, "@")[0]
		_ = emailSlugCleanRE.ReplaceAllString(strings.ToLower(slugBase), "-")
	}
}
