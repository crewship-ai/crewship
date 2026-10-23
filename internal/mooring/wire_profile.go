package mooring

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// MaxE3WireBytes is the negotiated mooring-e3-bounded-json-v1 body limit.
// It does not replace generic protocol maxima or imply large checkpoint support.
const MaxE3WireBytes = 262144

// CanonicalE3WireJSON bounds a complete UTF-8 JSON body before parsing, then
// bounds its normalized representation. Depth64 and structure32768 are shared
// with CanonicalJSON. Transport framing bytes are not part of the body.
// This unused primitive does NOT validate envelope schemas or grant authority;
// callers must validate the returned normalized bytes before any dispatch.
func CanonicalE3WireJSON(raw []byte) ([]byte, error) {
	if len(raw) > MaxE3WireBytes {
		return nil, ErrCanonicalInput
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil || len(canonical) > MaxE3WireBytes {
		return nil, ErrCanonicalInput
	}
	return canonical, nil
}

// ValidWireIdentifier counts Unicode scalar values, not UTF-8 bytes or UTF-16
// units. Opaque application text is not an identifier merely because of its key.
func ValidWireIdentifier(s string) bool {
	return utf8.ValidString(s) && len(s) > 0 && utf8.RuneCountInString(s) <= 200 && !strings.ContainsRune(s, 0)
}

var wireVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})$`)

// ValidWireVersion checks only frozen Versions syntax, never negotiated support.
func ValidWireVersion(s string) bool { return len(s) <= 29 && wireVersionPattern.MatchString(s) }
