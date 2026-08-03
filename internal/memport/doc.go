// Package memport moves agent memory across harness boundaries: out of
// Crewship into a portable bundle, and in from the two markdown-shaped
// ecosystems users arrive from (NanoClaw, OpenClaw).
//
// # Why a neutral middle
//
// Every format this package understands is "a directory of markdown
// files", and every one of them disagrees about which file means what.
// Rather than write N×M converters, each reader lowers its source into
// []Doc — a Crewship-canonical relative path plus a body — and each
// writer renders []Doc back out. Adding a format is one reader.
//
// # What this package deliberately does not do
//
//   - It does not write to disk. ReadSource produces a Plan; applying
//     that Plan goes through internal/memory.WriteFile, which owns the
//     caps, the scrubber, the flock and the version record. A second
//     door into the memory tree is exactly the failure mode the
//     chokepoint doctrine exists to prevent, and an importer is the
//     most tempting place to open one.
//
//   - It does not import derived data. Embeddings (OpenClaw's
//     vectors/), session transcripts (sessions/, data/sessions/) and
//     task logs are reconstructible or irrelevant, and importing a
//     foreign index would leave us with two indexes disagreeing. They
//     are reported in Plan.Skipped so an operator sees the omission
//     rather than assuming a total import.
//
//   - It does not guess a target. Which agent or crew receives the
//     memory is always supplied by the caller.
package memport

import "github.com/crewship-ai/crewship/internal/memory"

// Format is a memory layout this package can read.
type Format string

const (
	// FormatCrewship is our own on-disk layout: the canonical tier
	// files at the root of a .memory directory.
	FormatCrewship Format = "crewship"
	// FormatOKF is the Open Knowledge Format (Google Cloud, v0.1):
	// markdown with YAML frontmatter. Our export target, because it
	// is the only one of these that is a published spec rather than
	// one product's directory convention.
	FormatOKF Format = "okf"
	// FormatNanoClaw is groups/<channel>_<name>/CLAUDE.md.
	FormatNanoClaw Format = "nanoclaw"
	// FormatOpenClaw is the ~/.openclaw workspace: SOUL.md, USER.md,
	// MEMORY.md, memory/<date>.md and topic files.
	FormatOpenClaw Format = "openclaw"
)

// Doc is one memory document in the neutral middle representation.
//
// RelPath is Crewship-canonical and always forward-slashed: "AGENT.md",
// "daily/2026-08-01.md", "peers/pavel.md". It is a relative path under a
// .memory directory, never absolute — the caller decides which .memory.
type Doc struct {
	Tier    memory.Tier
	RelPath string
	Title   string
	Tags    []string
	// Sources lists the original paths that were merged to produce
	// this document, in the order they were merged. Several source
	// files routinely collapse into one canonical file (every OpenClaw
	// topic note becomes a section of AGENT.md), and an operator
	// reviewing a dry run needs to see which.
	Sources []string
	Body    []byte
}

// Skip records a source path that was read but deliberately not
// imported. Silence here would let a half-import read as a whole one.
type Skip struct {
	Source string
	Reason string
}

// Plan is what an import would do. It is the dry-run output and the
// input to the apply step; nothing between the two re-reads the source.
type Plan struct {
	Format  Format
	Docs    []Doc
	Skipped []Skip
}

// Options parameterise a read. The zero value is valid for sources that
// need no disambiguation.
type Options struct {
	// OperatorSlug names the person an operator-facing card belongs
	// to (OpenClaw's USER.md). Empty means "skip those files" — a peer
	// card filed under a guessed name is worse than no peer card.
	OperatorSlug string
	// Group picks one NanoClaw group when the source holds several.
	Group string
}
