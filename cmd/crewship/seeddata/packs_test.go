package seeddata

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Every pack file the seed promises to deliver must exist in the embed, and
// must land under shared/ — the only crew-files prefix the server maps into
// /crew/shared, which is where a routine's `script.path` resolves.
func TestPacks_FilesExistAndLandUnderShared(t *testing.T) {
	for _, p := range Packs {
		if len(p.Files) == 0 {
			t.Errorf("pack %s delivers no files — a pack without a deterministic core is just prose", p.Slug)
		}
		for _, f := range p.Files {
			if _, err := PackFileContent(f.Src); err != nil {
				t.Errorf("pack %s: %v", p.Slug, err)
			}
			if !strings.HasPrefix(f.Dest, "shared/") {
				t.Errorf("pack %s: dest %q must be under shared/", p.Slug, f.Dest)
			}
			if strings.Contains(f.Dest, "..") {
				t.Errorf("pack %s: dest %q must not traverse", p.Slug, f.Dest)
			}
		}
	}
}

// A pack names a crew, routines and a page that the other catalogues must
// actually contain. A typo here would seed a crew with scripts nobody calls.
func TestPacks_ReferencesResolve(t *testing.T) {
	crews := map[string]bool{}
	for _, c := range Crews {
		crews[c.Slug] = true
	}
	routines := map[string]RoutineDef{}
	for _, r := range Routines {
		routines[r.Slug] = r
	}
	pages := map[string]PageDef{}
	for _, pg := range Pages {
		pages[pg.Slug] = pg
	}
	seen := map[string]bool{}
	for _, p := range Packs {
		if seen[p.Slug] {
			t.Errorf("duplicate pack slug %q", p.Slug)
		}
		seen[p.Slug] = true
		if !crews[p.CrewSlug] {
			t.Errorf("pack %s: crew %q is not seeded", p.Slug, p.CrewSlug)
		}
		for _, slug := range []string{p.ProbeSlug, p.ReportSlug} {
			if slug == "" {
				continue
			}
			r, ok := routines[slug]
			if !ok {
				t.Errorf("pack %s: routine %q is not in the default routine catalogue", p.Slug, slug)
				continue
			}
			if r.CrewSlug != p.CrewSlug {
				t.Errorf("pack %s: routine %q belongs to crew %q, pack crew is %q — /crew/shared is per crew, the script would not be there",
					p.Slug, slug, r.CrewSlug, p.CrewSlug)
			}
		}
		if p.ReportSlug == "" {
			t.Errorf("pack %s: no report routine", p.Slug)
		}
		pg, ok := pages[p.PageSlug]
		if !ok {
			t.Errorf("pack %s: page %q is not seeded", p.Slug, p.PageSlug)
			continue
		}
		for _, panel := range pg.Panels {
			if panel.Producer != "routine/"+p.ReportSlug {
				t.Errorf("pack %s: page %s panel %s producer %q, want routine/%s",
					p.Slug, pg.Slug, panel.ID, panel.Producer, p.ReportSlug)
			}
		}
	}
}

// The probe of a pack is the wake gate — it has to be agentless, or the
// "costs nothing on a quiet night" claim in the docs is false.
func TestPacks_ProbeIsAgentless(t *testing.T) {
	for _, p := range Packs {
		if p.ProbeSlug == "" {
			continue
		}
		for _, r := range Routines {
			if r.Slug != p.ProbeSlug {
				continue
			}
			if v, _ := r.Definition["agentless"].(bool); !v {
				t.Errorf("pack %s: probe %s must declare agentless: true", p.Slug, p.ProbeSlug)
			}
			if c, _ := r.Definition["estimated_cost_usd"].(float64); c != 0 {
				t.Errorf("pack %s: probe %s estimated_cost_usd = %v, want 0", p.Slug, p.ProbeSlug, c)
			}
		}
	}
}

// Every script a routine step names must be one the pack delivers, at the
// path the step will resolve under /crew/shared.
func TestPacks_RoutineScriptsAreDelivered(t *testing.T) {
	for _, p := range Packs {
		delivered := map[string]bool{}
		for _, f := range p.Files {
			delivered[strings.TrimPrefix(f.Dest, "shared/")] = true
		}
		for _, r := range Routines {
			if r.Slug != p.ProbeSlug && r.Slug != p.ReportSlug {
				continue
			}
			steps, _ := r.Definition["steps"].([]map[string]interface{})
			for _, st := range steps {
				sc, ok := st["script"].(map[string]interface{})
				if !ok {
					continue
				}
				path, _ := sc["path"].(string)
				if !delivered[path] {
					t.Errorf("pack %s: routine %s step %v runs %q, which the pack does not deliver (files: %v)",
						p.Slug, r.Slug, st["id"], path, p.Files)
				}
			}
		}
	}
}

// The deterministic core of every pack ships with its own unit tests, and
// they run here so a change to a script is red in `go test` before it is
// wrong in a container. python3 is required, not optional: without it the
// test fails rather than skips, because a skip reads as a pass.
func TestPacks_ScriptUnitTestsPass(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		// A hard failure, not a skip: a skip reads as a pass, and the scripts
		// these suites cover run in every seeded workspace. Every CI runner
		// and every dev box carries python3.
		t.Fatalf("python3 not on PATH — the pack script suites cannot run: %v", err)
	}
	entries, err := fs.ReadDir(packsFS, "packs")
	if err != nil {
		t.Fatalf("read packs/: %v", err)
	}
	if len(entries) != len(Packs) {
		t.Errorf("packs/ has %d directories, Packs has %d entries — a pack directory with no catalogue entry (or the reverse)", len(entries), len(Packs))
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pack := e.Name()
		t.Run(pack, func(t *testing.T) {
			// The suites live on disk next to the scripts (they are not
			// embedded — the binary has no use for them). Resolve from the
			// package directory, which is the test's working directory.
			dir := filepath.Join("packs", pack)
			if _, err := os.Stat(filepath.Join(dir, "tests")); err != nil {
				t.Fatalf("pack %s has no tests/ directory", pack)
			}
			cmd := exec.Command(python, "-m", "unittest", "discover", "-s", "tests")
			cmd.Dir = dir
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("pack %s script suite failed:\n%s", pack, out)
			}
			if !strings.Contains(string(out), "OK") {
				t.Fatalf("pack %s script suite did not report OK:\n%s", pack, out)
			}
		})
	}
}

func TestMissingPackEnv(t *testing.T) {
	p := PackDef{RequiresEnv: []string{"A", "B"}}
	got := MissingPackEnv(p, func(k string) string {
		if k == "A" {
			return "set"
		}
		return ""
	})
	if got != "B" {
		t.Errorf("MissingPackEnv = %q, want B", got)
	}
	if got := MissingPackEnv(PackDef{}, os.Getenv); got != "" {
		t.Errorf("no requirements must report nothing missing, got %q", got)
	}
}
