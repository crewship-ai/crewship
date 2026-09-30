package convert

import (
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Default returns the maintained converter chain: one step per format
// version from backup.OldestRecoverableFormatVersion to
// backup.FormatVersion. Adding a FormatVersion means adding its step here
// (see the package doc and internal/backup/format.go).
func Default() *Registry {
	r, err := NewRegistry(stepV1toV2, stepV2toV3)
	if err != nil {
		panic(err) // static table; covered by tests
	}
	return r
}

// V1Tables is the fixed table list every v1 bundle's DB dump was limited
// to (internal/backup/dbdump.go BackupTables at the v1→v2 boundary,
// commit 76c0a4802^). v1 bundles never carried anything else.
var V1Tables = []string{
	"users", "workspaces", "crews", "agents", "skills", "agent_skills",
	"crew_members", "chats", "agent_mcp_bindings", "journal_entries",
}

// v1LegacyTableNames are names the earliest v1 writer listed that no
// migration ever created. The exporter skipped them, so a dump key with
// rows under one of them means a hand-made or damaged bundle.
var v1LegacyTableNames = []string{
	"crew_integrations", "mcp_bindings", "agent_chats",
	"memory_backups", "workspace_memory", "crew_memory",
}

// laterData names data the current writer carries that v1 bundles did
// not, in operator words. Only entries still in backup.BackupTables are
// reported, so the list cannot name a table the product no longer has.
var laterData = []struct{ what, table string }{
	{"issues", "missions"},
	{"issue comments", "mission_comments"},
	{"credentials", "credentials"},
	{"memory versions", "memory_versions"},
	{"workspace files", "workspace_files"},
	{"workspace members", "workspace_members"},
	{"routines", "pipelines"},
	{"inbox items", "inbox_items"},
	{"pages", "pages"},
}

var stepV1toV2 = Step{
	From:    1,
	Summary: "v2 changed restore semantics (--replace, FK-discovered table set); the payload layout is unchanged",
	Manifest: func(m *backup.Manifest, idx *PayloadIndex, r *StepReport) error {
		r.change("format_version 1 → 2; payload passes through unchanged (v2 made no layout change)")
		if m.ScopeLevel == "" {
			m.ScopeLevel = backup.ScopeLevelStandard
			r.change("scope_level was absent (the bundle predates presets); set to %q, which is exactly what pre-preset bundles collected", backup.ScopeLevelStandard)
		}
		r.change("schema_migration_versions kept as recorded, so restore still replays backfills for every migration newer than the bundle")

		if !idx.HasDBDump {
			return nil
		}
		v1 := map[string]bool{}
		for _, t := range V1Tables {
			v1[t] = true
		}
		var extra []string
		for t, n := range idx.Tables {
			if !v1[t] && n > 0 && !contains(v1LegacyTableNames, t) {
				extra = append(extra, t)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			r.warn("the v1 dump carries rows for tables outside the v1 table list (%s); they are kept and restore as usual", strings.Join(extra, ", "))
		}
		for _, t := range v1LegacyTableNames {
			if idx.Tables[t] > 0 {
				r.warn("%d row(s) under %q, a table name no Crewship schema ever had; kept, but restore skips them", idx.Tables[t], t)
			}
		}

		current := map[string]bool{}
		for _, t := range backup.BackupTables {
			current[t] = true
		}
		var missing []string
		for _, d := range laterData {
			if current[d.table] && idx.Tables[d.table] == 0 {
				missing = append(missing, d.what)
			}
		}
		if len(missing) > 0 {
			r.unrecoverable("v1 bundles carried only a fixed %d-table dump (%s); %s were never collected into this bundle and cannot be recovered from it",
				len(V1Tables), strings.Join(V1Tables, ", "), strings.Join(missing, ", "))
		}
		return nil
	},
}

var stepV2toV3 = Step{
	From:    2,
	Summary: "v3 added the crew/<slug>/ section (/crew memory trees) and made memory_included an observed flag",
	Manifest: func(m *backup.Manifest, idx *PayloadIndex, r *StepReport) error {
		r.change("format_version 2 → 3; payload passes through unchanged")
		var noMemory []string
		for i := range m.Contents.Crews {
			c := &m.Contents.Crews[i]
			output := idx.sectionFiles("memory", c.Slug) > 0
			crewFiles := idx.sectionFiles("crew", c.Slug) > 0
			memory := idx.CrewMemoryFiles[c.Slug] > 0
			if crewFiles {
				r.warn("crew %s: a crew/%s/ section is present in a pre-v3 bundle, which no v1/v2 writer produced; flags are set from the files observed", c.Slug, c.Slug)
			}
			if c.MemoryIncluded != memory {
				r.change("crew %s: memory_included %t → %t (before v3 it was set without looking and described /output, not the memory tree)", c.Slug, c.MemoryIncluded, memory)
			}
			if c.OutputIncluded != output {
				r.change("crew %s: output_included → %t, from the memory/%s/ (/output) section actually in the payload", c.Slug, output, c.Slug)
			}
			if c.CrewFilesIncluded != crewFiles {
				r.change("crew %s: crew_files_included → %t", c.Slug, crewFiles)
			}
			c.MemoryIncluded = memory
			c.OutputIncluded = output
			c.CrewFilesIncluded = crewFiles
			if !memory {
				noMemory = append(noMemory, c.Slug)
			}
		}
		if len(noMemory) > 0 {
			r.unrecoverable("v1/v2 bundles never contained /crew — the agents' and crews' .memory trees; crew %s: that memory is not in this bundle and cannot be recovered from it",
				strings.Join(noMemory, ", "))
		}
		return nil
	},
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// Describe renders a report as plain text, one line per fact, for the
// CLI's table output.
func Describe(r *Report) string {
	var b strings.Builder
	if len(r.Steps) == 0 {
		fmt.Fprintf(&b, "%s is already format v%d; nothing to convert, nothing written.\n", r.Source, r.To)
		return b.String()
	}
	fmt.Fprintf(&b, "Converted %s (v%d) → %s (v%d)\n", r.Source, r.From, r.Output, r.To)
	fmt.Fprintf(&b, "The original was not modified. Encryption: %s\n", r.Encryption)
	for _, s := range r.Steps {
		fmt.Fprintf(&b, "\nv%d → v%d: %s\n", s.From, s.To, s.Summary)
		for _, c := range s.Changes {
			fmt.Fprintf(&b, "  changed: %s\n", c)
		}
		for _, u := range s.Unrecoverable {
			fmt.Fprintf(&b, "  cannot be recovered: %s\n", u)
		}
	}
	if len(r.Warnings) > 0 {
		b.WriteString("\nWarnings:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(&b, "  %s\n", w)
		}
	}
	return b.String()
}
