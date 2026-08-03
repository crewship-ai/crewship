package memory

import (
	"path"
	"strings"
)

// CapForPath returns the byte ceiling that applies to a canonical
// memory-relative path ("AGENT.md", "daily/2026-08-01.md",
// "peers/pavel.md"), and whether the path is one this package
// recognises at all.
//
// A cap of 0 with ok=true means "recognised, deliberately uncapped"
// (lessons/learned), which is different from an unrecognised path — the
// caller must be able to tell those apart before deciding to write.
//
// # Why this exists
//
// The same ceilings were already spelled out in three places: the
// constants below, capForTier's tier-keyed switch in tools.go, and the
// sidecar's memoryWriteCaps map, the last carrying a "keep these in
// sync until the legacy path is retired" comment. Every writer that
// reaches memory from a new direction — the operator-driven import is
// one — would otherwise add a fourth. This is the path-keyed form the
// non-tier callers actually need.
func CapForPath(rel string) (int, bool) {
	clean := path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	switch clean {
	case "AGENT.md":
		return capAgentBytes, true
	case "CREW.md":
		return capCrewBytes, true
	case "PERSONA.md":
		return capPersonaBytes, true
	case "pins.md":
		return capPinsBytes, true
	case "lessons.md", "learned.md":
		return 0, true
	}
	// daily/<name>.md and peers/<name>.md — exactly one segment under
	// the directory, .md-suffixed. The tightness matters: it is what
	// stops "daily/../../x" or "peers/.env" from inheriting a cap and,
	// with it, an implicit blessing to be written.
	if rest, ok := strings.CutPrefix(clean, "daily/"); ok && isLeafMarkdown(rest) {
		return capDailyBytes, true
	}
	if rest, ok := strings.CutPrefix(clean, "peers/"); ok && isLeafMarkdown(rest) {
		return capPeerBytes, true
	}
	return 0, false
}

func isLeafMarkdown(rest string) bool {
	return !strings.Contains(rest, "/") &&
		strings.HasSuffix(rest, ".md") &&
		strings.TrimSuffix(rest, ".md") != ""
}
