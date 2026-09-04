package seeddata

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// A demo PACK is one real, repeatable use case seeded as a unit: the crew
// that runs it, the deterministic scripts it needs in /crew/shared, the
// routines that call them, the Page they publish to, and the issues that
// walk a human through it. Every pack follows the shape the market research
// in crewship-manifests settled on — a cheap probe that costs nothing, an
// expensive judgement only when there is something to judge, a human
// decision before anything irreversible — and every one of them can be run
// again tomorrow against the same real source and checked by
// `crewship seed verify`.
//
// The pack catalogue lives in Go rather than YAML because it is a set of
// RELATIONSHIPS (which crew, which routines, which page) between catalogues
// that already exist; the content itself stays where it always was —
// routines in routines_packs.go, pages in builtin/pages.yaml, issues in
// builtin/issues.yaml — so a reviewer of any one of those never has to open
// this file to understand it.
type PackDef struct {
	Slug        string
	Name        string
	Description string
	// CrewSlug is the ONE crew that runs the pack. One crew, deliberately:
	// /crew/shared is a per-crew mount, so a hand-off through files only
	// works inside a crew. A pack that needs two crews would need the API
	// between them, not the filesystem.
	CrewSlug string
	// Files are delivered to the crew's shared volume at seed time, before
	// provisioning starts — the host can still write the tree then.
	Files []PackFile
	// Routines the pack seeds, in the order they are meant to be run.
	// ProbeSlug is the token-zero wake gate (may be empty for a pack with no
	// gate); ReportSlug is the routine that produces the human-readable
	// result and publishes the page.
	ProbeSlug  string
	ReportSlug string
	// PageSlug is the Page the report routine writes; every panel on it
	// declares that routine as producer.
	PageSlug string
	// RequiresEnv lists the seed environment variables the pack needs to be
	// RUNNABLE (not merely seeded). A pack whose requirement is missing is
	// still seeded — crew, scripts, routines, page, issues — but
	// `seed verify` reports it as skipped with the reason, never as green.
	RequiresEnv []string
	// Sources are the real, public things the pack reads. Listed so the
	// showcase tests and the docs can say what the demo actually touches.
	Sources []string
}

// PackFile is one file delivered to the pack crew's shared volume.
type PackFile struct {
	// Src is the path inside the embedded packs/ tree.
	Src string
	// Dest is the crew-files path as `crewship crew files save` takes it —
	// always under shared/, which the server maps to /crew/shared in the
	// container.
	Dest string
}

//go:embed packs/*/scripts/* packs/*/config/*
var packsFS embed.FS

// Packs is the demo pack catalogue.
var Packs = []PackDef{
	{
		Slug: "ci-watch",
		Name: "Nightly CI watch",
		Description: "Watches the scheduled GitHub Actions workflows of crewship-ai/crewship. " +
			"Reports red runs AND the failure nobody sees: a workflow that silently stopped running. " +
			"A token-zero probe is the wake gate; the agent triage only runs when there is something to triage.",
		CrewSlug: "ops",
		Files: []PackFile{
			{Src: "packs/ci-watch/scripts/ci_probe.py", Dest: "shared/scripts/ci_probe.py"},
		},
		ProbeSlug:   "ci-probe",
		ReportSlug:  "ci-nightly-triage",
		PageSlug:    "ci-watch",
		RequiresEnv: []string{"SEED_GITHUB_TOKEN"},
		Sources:     []string{"https://api.github.com/repos/crewship-ai/crewship/actions"},
	},
	{
		Slug: "docs-drift",
		Name: "Docs drift audit",
		Description: "Audits documentation against code in both directions: pages that describe " +
			"configuration the code does not have, and schema fields the docs never mention. " +
			"A deterministic scan proposes candidates; the agent confirms or rejects each one with a path and a line.",
		CrewSlug: "quality",
		Files: []PackFile{
			{Src: "packs/docs-drift/scripts/docs_drift.py", Dest: "shared/scripts/docs_drift.py"},
			{Src: "packs/docs-drift/scripts/docs_audit.sh", Dest: "shared/scripts/docs_audit.sh"},
			{Src: "packs/docs-drift/config/docs_map.json", Dest: "shared/config/docs_map.json"},
		},
		ProbeSlug:   "",
		ReportSlug:  "docs-drift-audit",
		PageSlug:    "docs-drift",
		RequiresEnv: []string{"SEED_GITHUB_TOKEN"},
		Sources:     []string{"https://github.com/crewship-ai/crewship"},
	},
	{
		Slug: "site-replica",
		Name: "Site replica",
		Description: "A lead delegates the copy of a real public home page (www.seznam.cz) across an " +
			"analyst, a data engineer, a frontend engineer and a tester in one crew. A deterministic " +
			"acceptance check says whether the replica meets the bar; a human judges whether it looks right.",
		CrewSlug: "engineering",
		Files: []PackFile{
			{Src: "packs/site-replica/scripts/replica_check.py", Dest: "shared/scripts/replica_check.py"},
		},
		ProbeSlug:   "",
		ReportSlug:  "site-replica-audit",
		PageSlug:    "site-replica",
		RequiresEnv: nil,
		Sources:     []string{"https://www.seznam.cz"},
	},
}

// PackBySlug returns the pack with the given slug.
func PackBySlug(slug string) (PackDef, bool) {
	for _, p := range Packs {
		if p.Slug == slug {
			return p, true
		}
	}
	return PackDef{}, false
}

// PackFileContent returns the bytes of one embedded pack file.
func PackFileContent(src string) ([]byte, error) {
	b, err := fs.ReadFile(packsFS, src)
	if err != nil {
		return nil, fmt.Errorf("seeddata: pack file %s: %w", src, err)
	}
	return b, nil
}

// PackRoutineSlugs lists every routine slug the packs seed, probes first.
func PackRoutineSlugs() []string {
	var out []string
	for _, p := range Packs {
		if p.ProbeSlug != "" {
			out = append(out, p.ProbeSlug)
		}
		if p.ReportSlug != "" {
			out = append(out, p.ReportSlug)
		}
	}
	return out
}

// PackForRoutine returns the pack that owns a routine slug, if any.
func PackForRoutine(slug string) (PackDef, bool) {
	for _, p := range Packs {
		if p.ProbeSlug == slug || p.ReportSlug == slug {
			return p, true
		}
	}
	return PackDef{}, false
}

// MissingPackEnv returns the RequiresEnv entries that are unset in the
// environment described by getenv, joined for a log line — empty when the
// pack is fully runnable.
func MissingPackEnv(p PackDef, getenv func(string) string) string {
	var missing []string
	for _, k := range p.RequiresEnv {
		if strings.TrimSpace(getenv(k)) == "" {
			missing = append(missing, k)
		}
	}
	return strings.Join(missing, ", ")
}
