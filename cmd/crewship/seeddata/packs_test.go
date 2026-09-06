package seeddata

import (
	"encoding/json"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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

// ── the failure panel contract ────────────────────────────────────────────
//
// A pack script is written to survive its own bad day: when it cannot read
// its source it still exits ZERO and prints a JSON document, so the routine
// keeps running and the page keeps saying something true. That degrade only
// works if the failure document carries EVERY field the routine projects out
// of it — a `transform` step whose field is missing does not degrade, it
// FAILS the run, and with it the agent step and every page panel behind it.
// (docs-drift shipped exactly that: two `fail()` paths without `sha_label`,
// so a scan that could not check the repository out took the whole audit
// down with it.)
//
// The projected fields are derived from the routine definitions themselves,
// so a transform step added tomorrow is covered the day it is added.

var stepOutputRefRe = regexp.MustCompile(`\{\{\s*steps\.([A-Za-z0-9_.-]+)\.output\s*\}\}`)

// transformProjections maps a step id to the expressions other steps project
// out of that step's output (".panel.state", ".total_candidates", …).
func transformProjections(def map[string]interface{}) map[string][]string {
	out := map[string][]string{}
	steps, _ := def["steps"].([]map[string]interface{})
	for _, st := range steps {
		if t, _ := st["type"].(string); t != "transform" {
			continue
		}
		tr, _ := st["transform"].(map[string]interface{})
		if tr == nil {
			continue
		}
		input, _ := tr["input"].(string)
		expr, _ := tr["expression"].(string)
		m := stepOutputRefRe.FindStringSubmatch(input)
		if m == nil || expr == "" {
			continue
		}
		out[m[1]] = append(out[m[1]], expr)
	}
	return out
}

// scriptStepPaths maps a step id to the `script.path` that step runs.
func scriptStepPaths(def map[string]interface{}) map[string]string {
	out := map[string]string{}
	steps, _ := def["steps"].([]map[string]interface{})
	for _, st := range steps {
		sc, _ := st["script"].(map[string]interface{})
		if sc == nil {
			continue
		}
		id, _ := st["id"].(string)
		path, _ := sc["path"].(string)
		if id != "" && path != "" {
			out[id] = path
		}
	}
	return out
}

// packScriptProjections returns, per pack script (keyed by its embedded
// source path), every expression the pack's routines project out of that
// script's output. A script two routines run gets the UNION: one failure
// document has to satisfy both of them.
func packScriptProjections(p PackDef) map[string][]string {
	src := map[string]string{}
	for _, f := range p.Files {
		src[strings.TrimPrefix(f.Dest, "shared/")] = f.Src
	}
	seen := map[string]map[string]bool{}
	for _, r := range Routines {
		if r.Slug != p.ProbeSlug && r.Slug != p.ReportSlug {
			continue
		}
		paths := scriptStepPaths(r.Definition)
		proj := transformProjections(r.Definition)
		for id, path := range paths {
			file := src[path]
			if file == "" {
				continue // TestPacks_RoutineScriptsAreDelivered owns this
			}
			for _, expr := range proj[id] {
				if seen[file] == nil {
					seen[file] = map[string]bool{}
				}
				seen[file][expr] = true
			}
		}
	}
	out := map[string][]string{}
	for file, exprs := range seen {
		for e := range exprs {
			out[file] = append(out[file], e)
		}
		sort.Strings(out[file])
	}
	return out
}

// jsonFieldPresent resolves a transform expression (".panel.sha_label")
// against a decoded document. A null is not present: the transform would
// project nothing and the page row would render blank.
func jsonFieldPresent(doc map[string]any, expr string) bool {
	var cur any = doc
	for _, seg := range strings.Split(strings.TrimPrefix(expr, "."), ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur, ok = m[seg]
		if !ok || cur == nil {
			return false
		}
	}
	return true
}

// degradeRun is one way to drive a pack script down a path where it cannot
// do its job. It must still exit 0 and print the whole document.
type degradeRun struct {
	name string
	cmd  *exec.Cmd
}

// refusedAddr is a 127.0.0.1 address nothing listens on: a connection to it
// is refused immediately, so a probe's "the API is unreachable" branch runs
// without touching the network (and without hanging where there is none).
func refusedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close the reserved port: %v", err)
	}
	return addr
}

// minimalEnv is an explicit environment, never the test process's own: the
// dev box that runs this suite has GH_TOKEN set, and inheriting it would
// walk the scripts down their HAPPY path over the real network.
func minimalEnv(extra ...string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LC_ALL=C.UTF-8"}
	return append(env, extra...)
}

// degradeRunsFor builds the failure invocations of one pack script. A script
// with no recipe here is a hard failure, not a skip: an unexercised degrade
// path is precisely the one that breaks in a container at 03:00.
func degradeRunsFor(t *testing.T, pack, script string) []degradeRun {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 not on PATH: %v", err)
	}
	switch filepath.Base(script) {
	case "ci_probe.py":
		proxy := "http://" + refusedAddr(t)
		cmd := exec.Command(python, script, "--repo", "crewship-ai/crewship")
		cmd.Env = minimalEnv("GH_TOKEN=not-a-real-token",
			"http_proxy="+proxy, "https_proxy="+proxy, "HTTP_PROXY="+proxy, "HTTPS_PROXY="+proxy)
		return []degradeRun{{name: "GitHub unreachable", cmd: cmd}}

	case "replica_check.py":
		cmd := exec.Command(python, script, "--dir", filepath.Join(t.TempDir(), "never-built"))
		cmd.Env = minimalEnv()
		return []degradeRun{{name: "no replica built", cmd: cmd}}

	case "docs_audit.sh":
		bash, err := exec.LookPath("bash")
		if err != nil {
			t.Fatalf("bash not on PATH: %v", err)
		}
		validMap := `{"pairs":[{"doc":"docs/x.mdx","pkg":["internal/x"]}]}`
		// A shared root shaped like the crew volume (config/ + scripts/),
		// so the guards can be knocked over one at a time.
		shared := func(mapJSON string, withDrift bool) string {
			dir := t.TempDir()
			for _, sub := range []string{"config", "scripts"} {
				if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if mapJSON != "" {
				if err := os.WriteFile(filepath.Join(dir, "config", "docs_map.json"), []byte(mapJSON), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if withDrift {
				b, err := os.ReadFile(filepath.Join("packs", "docs-drift", "scripts", "docs_drift.py"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "scripts", "docs_drift.py"), b, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			return dir
		}
		run := func(name string, env ...string) degradeRun {
			cmd := exec.Command(bash, script)
			cmd.Env = minimalEnv(env...)
			return degradeRun{name: name, cmd: cmd}
		}
		return []degradeRun{
			// The shell guards.
			run("no GH_TOKEN", "SHARED_ROOT="+shared("", false)),
			run("docs map missing", "GH_TOKEN=x", "SHARED_ROOT="+shared("", false)),
			run("docs_drift.py missing", "GH_TOKEN=x", "SHARED_ROOT="+shared(validMap, false)),
			// The python scan's own guards, reached through LOCAL_REPO so
			// that neither git nor the network is involved.
			run("docs map is not JSON", "LOCAL_REPO="+t.TempDir(), "SHARED_ROOT="+shared("{ not json", true)),
			run("docs map has no pairs", "LOCAL_REPO="+t.TempDir(), "SHARED_ROOT="+shared(`{"note":"no pairs here"}`, true)),
		}
	}
	t.Fatalf("pack %s: no failure recipe for %s — an unexercised degrade path is the one that breaks in production", pack, script)
	return nil
}

func TestPacks_FailureOutputCarriesEveryProjectedField(t *testing.T) {
	for _, p := range Packs {
		p := p
		projections := packScriptProjections(p)
		if len(projections) == 0 {
			t.Errorf("pack %s: no routine projects anything out of a script — either the pack lost its deterministic core, or this test stopped finding it", p.Slug)
			continue
		}
		for script, exprs := range projections {
			script, exprs := script, exprs
			t.Run(p.Slug+"/"+filepath.Base(script), func(t *testing.T) {
				for _, dr := range degradeRunsFor(t, p.Slug, script) {
					dr := dr
					t.Run(dr.name, func(t *testing.T) {
						var stderr strings.Builder
						dr.cmd.Stderr = &stderr
						out, err := dr.cmd.Output()
						if err != nil {
							t.Fatalf("%s exited non-zero (%v) — a script step that exits non-zero fails the routine; the degrade has to be JSON on stdout and exit 0\nstderr: %s",
								script, err, stderr.String())
						}
						var doc map[string]any
						if err := json.Unmarshal(out, &doc); err != nil {
							t.Fatalf("%s did not print one JSON document: %v\nstdout: %s\nstderr: %s", script, err, out, stderr.String())
						}
						for _, expr := range exprs {
							if !jsonFieldPresent(doc, expr) {
								t.Errorf("%s: the %q path omits %s, which the routine projects with a transform step — that transform would fail the whole run\ndocument: %s",
									script, dr.name, expr, out)
							}
						}
					})
				}
			})
		}
	}
}
