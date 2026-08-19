package memory

// Projection — which memory files reach the memory_versions audit
// trail, and which ones a reader must be told they cannot see.
//
// Every operator-facing memory surface (the agent canvas Memory tab,
// `crewship memory log`, the admin drill-down) reads memory_versions.
// That table is a PROJECTION of the .memory tree, not the tree itself:
// a file is only in it if some writer recorded it. Three writers do:
//
//	audit watcher   every canonical file under {base}/crews/**/.memory
//	                (audit_watcher.go — the parser below is its gate)
//	consolidator    pins.md + learned-*.md at crew:{crewID}/{file}
//	                (internal/consolidate, canonicalAuditPath)
//	restore         a replay of an existing row (versions.go Restore)
//
// Nothing else does. In particular lessons.md is written host-side by
// consolidate.WriteLesson and PERSONA.md / peer cards have their own
// endpoints — none of them lands here.
//
// The consequence is the defect §4.5 of the 2026-08-13 chat-surface
// audit names: a tier that is not projected returns an empty list,
// which a UI renders identically to "this agent has written nothing".
// PathProjection lets the API say which of the two it is, so the panel
// can refuse to render silence as fact.

import (
	"path"
	"strings"
)

// memoryScope distinguishes the two subtrees a crew directory holds.
// The layout is fixed by the docker provider's bind mounts and stated
// once in crewpaths.go:
//
//	{base}/crews/{crewID}/agents/{slug}/.memory   scopeAgent
//	{base}/crews/{crewID}/shared/.memory          scopeShared
type memoryScope int

const (
	scopeAgent memoryScope = iota
	scopeShared
)

// classifyMemoryFile maps a path relative to a .memory root onto the
// tier its versions are recorded under. ok=false means "no writer
// projects this file" — the audit watcher skips it, and PathProjection
// reports it as unreadable rather than empty. Both callers share this
// function so the watcher's gate and the API's honesty signal cannot
// drift apart.
//
// Rules, by scope:
//
//	scopeAgent   AGENT.md, CREW.md, pins.md, learned-*.md, daily/*.md
//	scopeShared  CREW.md, pins.md, learned-*.md, daily/*.md
//
// A dot-prefixed segment anywhere in the path is refused outright:
// .proposed/ is the consolidator's staging area (approve.go records
// the merge, the proposal is not canonical), .quarantine/ holds
// content the injection scanner REFUSED, and .snapshots/ is the
// orchestrator's pre-run copy. Recording any of them would put
// non-canonical bytes in the audit trail under a canonical name.
func classifyMemoryFile(scope memoryScope, memoryRel string) (Tier, bool) {
	memoryRel = strings.TrimPrefix(path.Clean(memoryRel), "./")
	if memoryRel == "" || memoryRel == "." {
		return "", false
	}
	segs := strings.Split(memoryRel, "/")
	for _, s := range segs {
		if s == "" || strings.HasPrefix(s, ".") {
			return "", false
		}
	}
	base := segs[len(segs)-1]

	// daily/<name>.md, directly under the .memory root. The tier says
	// whose day it is: an agent's own log is personal (TierAgent), the
	// crew-shared one belongs to the crew.
	if len(segs) == 2 && segs[0] == "daily" && strings.HasSuffix(base, ".md") {
		if scope == scopeShared {
			return TierCrew, true
		}
		return TierAgent, true
	}

	switch {
	case base == "AGENT.md":
		// An agent's own canonical file. There is no such thing in the
		// shared tree — every agent has its own.
		if scope == scopeShared {
			return "", false
		}
		return TierAgent, true
	case base == "CREW.md":
		return TierCrew, true
	case base == "pins.md":
		return TierPins, true
	case strings.HasPrefix(base, "learned-") && strings.HasSuffix(base, ".md"):
		return TierLearned, true
	}
	return "", false
}

// ProjectionState is the three-way answer to "can this surface show
// the history of this path?".
type ProjectionState string

const (
	// ProjectionRecorded — a writer records this path. An empty list
	// therefore means the file has not been written yet, which is a
	// fact the reader may act on.
	ProjectionRecorded ProjectionState = "recorded"
	// ProjectionUnrecorded — no writer records this path. An empty
	// list says nothing at all about the file on disk, and a surface
	// that renders it as an empty history is lying.
	ProjectionUnrecorded ProjectionState = "unrecorded"
	// ProjectionUnavailable — versioning is switched off on this
	// server (no blob root configured), so NO path can be recorded,
	// however well-formed. Same reading rule as unrecorded, different
	// cause and different fix.
	ProjectionUnavailable ProjectionState = "unavailable"
)

// Projection is the wire shape the version-list endpoint returns
// alongside the rows, so a client can tell an empty history from an
// unreadable one without hard-coding this package's knowledge (which
// goes stale — the 2026-08-13 audit found three documents that had).
type Projection struct {
	State  ProjectionState `json:"state"`
	Reason string          `json:"reason"`
}

// Readable reports whether an empty entry list from this path may be
// read as "nothing has been written".
func (p Projection) Readable() bool { return p.State == ProjectionRecorded }

// ProjectionUnavailableReason is the answer for every path when the
// server has no blob root: RecordVersion refuses without one, so the
// trail is empty by configuration rather than by fact.
const ProjectionUnavailableReason = "Memory versioning is switched off on this server (no blob root is " +
	"configured), so no write of any tier is recorded. The files on disk are unaffected — " +
	"this history simply cannot be collected."

// PathProjection answers whether auditPath — a canonical
// memory_versions path, "agent:{slug}/{rel}" or "crew:{crewID}/{rel}"
// — is one that some writer records.
//
// It classifies the path, it does not query the DB: the question is
// "could a row for this ever exist", which is a property of the
// writers, not of the current contents of the table.
func PathProjection(auditPath string) Projection {
	auditPath = strings.TrimSpace(auditPath)
	scope, rel, ok := splitAuditPath(auditPath)
	if !ok {
		return Projection{
			State: ProjectionUnrecorded,
			Reason: "Not a canonical memory path. The audit trail is keyed by " +
				"'agent:{slug}/{file}' or 'crew:{crewID}/{file}'; nothing else is recorded.",
		}
	}
	if _, ok := classifyMemoryFile(scope, rel); ok {
		return Projection{
			State: ProjectionRecorded,
			Reason: "Recorded by the memory audit watcher on every write, and by the " +
				"consolidator for the files it maintains.",
		}
	}
	return Projection{State: ProjectionUnrecorded, Reason: unrecordedReason(rel)}
}

// splitAuditPath pulls the scope prefix off a canonical audit path.
func splitAuditPath(auditPath string) (memoryScope, string, bool) {
	for _, p := range []struct {
		prefix string
		scope  memoryScope
	}{
		{"agent:", scopeAgent},
		{"crew:", scopeShared},
	} {
		rest, found := strings.CutPrefix(auditPath, p.prefix)
		if !found {
			continue
		}
		owner, rel, ok := strings.Cut(rest, "/")
		if !ok || owner == "" || rel == "" {
			return 0, "", false
		}
		return p.scope, rel, true
	}
	return 0, "", false
}

// unrecordedReason names the writer that owns the file, where one
// exists, so the reader is told where the content actually is rather
// than only that it is missing here.
func unrecordedReason(rel string) string {
	base := path.Base(rel)
	switch {
	case base == "lessons.md":
		return "lessons.md is written by the negative-learning evaluator " +
			"(consolidate.WriteLesson) straight to the memory directory, and no writer " +
			"projects it into the version trail. There is no row to list and no endpoint " +
			"that reads it — the file may well have content. Read it from inside the crew."
	case base == "PERSONA.md":
		return "PERSONA has its own history: GET /api/v1/agents/{id}/persona/history. " +
			"It is not projected into the memory version trail."
	case strings.HasPrefix(rel, "peers/"):
		return "Peer cards are listed by GET /api/v1/agents/{id}/peers, not by the " +
			"version trail."
	}
	return "No writer records this path into the memory version trail, so an empty " +
		"history here says nothing about what is on disk."
}
