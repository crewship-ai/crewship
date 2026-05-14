package api

import (
	"path/filepath"
	"strings"
)

// normalizeRequestPath cleans and validates a path that came from the
// query string and is about to be used as a relative path inside a
// constrained directory (typically the agent's storage root). Returns
// the cleaned path and ok=true when the path is safe; ok=false signals
// the caller should reject the request with 400 "Invalid file path".
//
// Pre-fix the four call sites in proxy_files.go each had a copy-pasted
// `filepath.Clean` + HasPrefix("..") + IsAbs check. The four copies
// drifted in subtle ways — the URL-decoded form was sometimes validated
// before Clean, sometimes after, and a double-encoded payload like
// %2e%2e%2fetc%2fpasswd took a different branch and surfaced as 404
// instead of 400. The inconsistency was cosmetic in this build but
// would become exploitable the moment any one site changed its
// decoding step. Centralising the check forces all sites to stay
// in sync.
//
// Rejection rules — applied to the URL-decoded form (Go's net/url
// already decodes the query parameter once for us):
//   - empty path
//   - absolute paths (filepath.IsAbs)
//   - any cleaned path that starts with ".." (escapes the base)
//   - paths whose Clean form contains a "../" segment anywhere (extra
//     defense for engines that follow symlinks mid-path)
//   - paths containing NUL bytes (defense against C-string truncation
//     in any downstream Linux syscall)
//   - paths still containing the URL-encoded forms of "../" — these
//     mean a layer downstream will decode again, which is the bug
//     pattern exploited by double-encoding to bypass naive Clean checks
func normalizeRequestPath(in string) (string, bool) {
	if in == "" {
		return "", false
	}
	if strings.ContainsRune(in, 0) {
		return "", false
	}
	// Reject still-encoded escape sequences. Net/url decodes once; if
	// any look-alike survives, a downstream consumer will decode it
	// again and we'd be back to the bypass.
	low := strings.ToLower(in)
	for _, marker := range []string{"%2e%2e", "%2f", "%5c", "..%2f", "..%5c"} {
		if strings.Contains(low, marker) {
			return "", false
		}
	}
	cleaned := filepath.Clean(in)
	if filepath.IsAbs(cleaned) {
		return "", false
	}
	// HasPrefix("..") covers `..` and `../foo`. We also explicitly check
	// for `../` anywhere in the cleaned form — Clean usually collapses
	// this away, but on Windows the separator differs and on some inputs
	// (`a/../b/../..`) the result still contains a leading `..`.
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.HasPrefix(cleaned, "..\\") {
		return "", false
	}
	if strings.Contains(cleaned, "/../") || strings.Contains(cleaned, "\\..\\") {
		return "", false
	}
	return cleaned, true
}
