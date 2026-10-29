//go:build linux

package api

import (
	"unicode/utf8"

	"github.com/gowebpki/jcs"
)

// These private C1 profiles do not widen legacy operation-envelope/HTTP JSON.
// Checkpoints retain the existing 4MiB product bound; private RPC retains its
// existing 32MiB frame bound (quoted checkpoint strings add wire overhead).
// Depth64 and strict UTF8/duplicate rejection match the legacy JCS profile.
const commandNativeJSONStructureLimit = 1 << 20
const commandNativeJSONDepthLimit = 64

func commandCheckpointJSON(raw []byte) ([]byte, error) {
	return commandPrivateCanonicalJSON(raw, commandCheckpointLimit)
}
func commandPrivateFrameJSON(raw []byte) ([]byte, error) {
	return commandPrivateCanonicalJSON(raw, commandNativeFrameLimit)
}
func commandPrivateCanonicalJSON(raw []byte, limit int) ([]byte, error) {
	// Admission bounds precede recursive parsing/canonicalization allocations.
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) {
		return nil, errCommandStore
	}
	depth, structure := 0, 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '{' || b == '[' || b == ',' || b == ':' {
			structure++
			if structure > commandNativeJSONStructureLimit {
				return nil, errCommandStore
			}
		}
		switch b {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > commandNativeJSONDepthLimit {
				return nil, errCommandStore
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return nil, errCommandStore
			}
		}
	}
	// JCS remains the RFC8785 implementation and strict syntax/duplicate check;
	// no checkpoint bytes are rewritten for canonical SQL identity or storage.
	canonical, err := jcs.Transform(raw)
	if err != nil || len(canonical) > limit {
		return nil, errCommandStore
	}
	return canonical, nil
}
