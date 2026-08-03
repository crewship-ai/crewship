package memport

import (
	"path"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/memory"
)

// What an import is allowed to write.
//
// Every other write door into a memory tree answers this question with a
// closed set — the sidecar's memoryWriteCaps plus its daily/<name>.md
// rule, the dispatcher's tier enum. An importer that accepts whatever
// relative path it is handed is a way to put files into the tree that no
// other surface in the product would have taken: a `.quarantine/<sha>.md`
// reinstating content the quarantine deliberately isolated, or a
// `daily/<date>/notes.md` nested forgery of the shape the sidecar was
// fixed to reject. Both would then be walked by the FTS indexer and come
// back out of memory.search into an agent's context.
//
// So the rule here is the same as everywhere else: recognised, or
// refused with a reason.

// importRefusal explains why a path may not be written. Empty means the
// path is accepted.
type importRefusal string

const (
	refusalUnknown      importRefusal = "not a recognised memory file"
	refusalConsolidator importRefusal = "owned by the consolidator — it carries a YAML schema and its own locking; importing freeform markdown would destroy the store"
	refusalQuarantine   importRefusal = "quarantined content is isolated on purpose and must not be reinstated by an import"
	refusalCrewOnly     importRefusal = "the <crew>/topics/ tree exists only in crew-shared memory; import it with a crew target"
)

// checkImportPath decides whether rel may be written and, if so, under
// what byte ceiling.
//
// The ceiling comes from memory.CapForPath so an import lands under the
// same limits an agent's own writes do. Where CapForPath says
// "recognised, uncapped" (the consolidator's files) this refuses
// instead: uncapped is a statement about the writer that owns the file,
// not permission for a different writer to replace it.
func checkImportPath(rel string, scope Scope) (cap int, refusal importRefusal) {
	slashed := strings.ReplaceAll(rel, "\\", "/")
	clean := path.Clean(slashed)

	if clean == "." || clean == "" || strings.HasPrefix(clean, "..") || path.IsAbs(clean) {
		return 0, refusalUnknown
	}
	// The path must already be canonical. "topics/../AGENT.md" cleans to
	// something safe, but accepting it means the file written is not the
	// file the payload named — and no legitimate producer emits that
	// shape. Requiring the canonical form keeps "what you see in the
	// plan is what lands on disk" true.
	if clean != slashed {
		return 0, refusalUnknown
	}
	if hasSegment(clean, ".quarantine") {
		return 0, refusalQuarantine
	}
	if isConsolidatorOwned(clean) {
		return 0, refusalConsolidator
	}

	// The crew's pinned notes live one level down, under
	// <crew-slug>/topics/pins.md (crewpaths.go HostCrewTopicsDir). It is
	// the one nested shape the importer accepts, because it is the one
	// nested shape the product itself writes and a crew export would
	// otherwise not round-trip.
	// The crew's pinned notes live one level down, at
	// <crew-slug>/topics/pins.md (crewpaths.go HostCrewTopicsDir). That
	// shape is legitimate ONLY in a crew tree — HostCrewTopicsDir never
	// produces it under an agent root — so the scope decides, not the
	// spelling. Accepting it on a character-class match alone let an
	// import invent a directory inside an agent's memory, which the FTS
	// indexer would then walk and serve back into that agent's context.
	if segs := strings.Split(clean, "/"); len(segs) == 3 &&
		segs[1] == "topics" && segs[2] == "pins.md" && crewSlugRe.MatchString(segs[0]) {
		if scope != ScopeCrew {
			return 0, refusalCrewOnly
		}
		c, _ := memory.CapForPath("pins.md")
		return c, ""
	}

	c, known := memory.CapForPath(clean)
	if !known {
		return 0, refusalUnknown
	}
	if c == 0 {
		// Recognised but uncapped means a dedicated writer owns it.
		return 0, refusalConsolidator
	}
	return c, ""
}

// crewSlugRe bounds the first segment to something that can actually be
// a crew slug. Without it "any directory called topics" was the rule,
// which let an import invent directories inside the memory tree.
var crewSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// isConsolidatorOwned matches the files consolidate.WriteLesson and the
// learned-rules sweep maintain: lessons.md, learned.md and the
// learned-<topic>.md family the audit watcher already knows about.
func isConsolidatorOwned(clean string) bool {
	base := path.Base(clean)
	if strings.EqualFold(base, "lessons.md") || strings.EqualFold(base, "learned.md") {
		return true
	}
	return strings.HasPrefix(base, "learned-") && strings.HasSuffix(base, ".md")
}

func hasSegment(clean, want string) bool {
	for _, seg := range strings.Split(clean, "/") {
		if seg == want {
			return true
		}
	}
	return false
}
