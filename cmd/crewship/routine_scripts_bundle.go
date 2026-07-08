package main

// Portable routine bundles: inline a routine's `type: script` files into the
// export bundle and re-materialize them on import, so a routine travels with
// its deterministic backbone (the moat: "recipe + scripts + agent judgment").
//
// Scripts remain a CREW ASSET — the crew manifest `files:` block is the source
// of truth and the primary delivery path (materialized at `crewship apply`).
// Export/import inlining is a portability convenience layered on top: it reuses
// the exact same `/crews/{id}/files/{download,save}` endpoints the crew-files
// CLI uses, so there is no second storage channel.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// scriptEntry is one inlined script in a bundle's top-level `scripts` array.
// Path is the script-step path exactly as declared in the DSL; ContentB64 is
// the file's bytes, base64-encoded (the bundle is JSON, so binary-safe).
type scriptEntry struct {
	Path       string `json:"path"`
	ContentB64 string `json:"content_b64"`
	Size       int    `json:"size,omitempty"`
}

// decodeBundleScripts extracts the top-level `scripts` array from a decoded
// bundle (map form) into typed entries. A bundle without scripts yields nil.
func decodeBundleScripts(bundle map[string]any) ([]scriptEntry, error) {
	v, ok := bundle["scripts"]
	if !ok || v == nil {
		return nil, nil
	}
	// Re-marshal the generic value and decode into the typed shape — robust
	// against key ordering and avoids hand-walking any-typed maps.
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("re-encode bundle scripts: %w", err)
	}
	var entries []scriptEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("bundle `scripts` is malformed: %w", err)
	}
	return entries, nil
}

// crewFileIO is the minimal surface inline/materialize need against a crew's
// shared directory. Backed by the live client in production; faked in tests.
type crewFileIO interface {
	// download returns the file bytes and whether it exists. A missing file
	// is (nil, false, nil) — NOT an error — so callers distinguish absent
	// from a transport failure.
	download(crewID, crewPath string) ([]byte, bool, error)
	save(crewID, crewPath string, data []byte) error
}

// b64 encodes bytes/string for a bundle entry (test + impl share it).
func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// collectScriptPaths parses a routine DSL definition and returns the sorted,
// unique set of `type: script` step paths (as declared). Non-script steps
// contribute nothing. Parse (not Validate) so an export doesn't require the
// receiving-workspace agent slugs to resolve.
func collectScriptPaths(definition []byte) ([]string, error) {
	dsl, err := pipeline.Parse(definition)
	if err != nil {
		return nil, fmt.Errorf("parse routine definition: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range dsl.Steps {
		if s.Type != pipeline.StepScript || s.Script == nil {
			continue
		}
		p := strings.TrimSpace(s.Script.Path)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out, nil
}

// scriptCrewFilePath maps a script-step path to the crew shared-file path the
// `/files/{download,save}` endpoints expect ("shared/…"). Mirrors the runner's
// path fence (internal/pipeline/runner_script.go resolveScriptPath) and the
// manifest crew-file normalizer: the path must resolve strictly under the crew
// shared root; traversal or an absolute escape is rejected.
func scriptCrewFilePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("empty script path")
	}
	var abs string
	if strings.HasPrefix(p, "/") {
		abs = path.Clean(p)
	} else if strings.HasPrefix(p, "shared/") {
		// already crew-file-relative
		abs = path.Clean("/crew/" + p)
	} else {
		abs = path.Clean("/crew/shared/" + p)
	}
	const root = "/crew/shared/"
	if !strings.HasPrefix(abs, root) {
		return "", fmt.Errorf("script path %q must resolve under %s (no traversal, no absolute escape)", p, root)
	}
	// /crew/shared/scripts/x.py -> shared/scripts/x.py
	return strings.TrimPrefix(abs, "/crew/"), nil
}

// inlineScripts downloads each of the routine's script-step files from the
// author crew and returns bundle entries. A referenced script that is absent
// from the author crew is a hard error — exporting a routine whose backbone
// isn't materialized would produce a silently-incomplete bundle.
func inlineScripts(io crewFileIO, authorCrewID string, definition []byte) ([]scriptEntry, error) {
	paths, err := collectScriptPaths(definition)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(authorCrewID) == "" {
		return nil, fmt.Errorf("routine references %d script step(s) but has no author crew to inline them from", len(paths))
	}
	var entries []scriptEntry
	for _, p := range paths {
		crewPath, err := scriptCrewFilePath(p)
		if err != nil {
			return nil, err
		}
		data, ok, err := io.download(authorCrewID, crewPath)
		if err != nil {
			return nil, fmt.Errorf("download script %q from author crew: %w", p, err)
		}
		if !ok {
			return nil, fmt.Errorf("script %q not found in author crew shared dir (%s) — deliver it via the crew manifest `files:` block and `crewship apply` before exporting", p, crewPath)
		}
		entries = append(entries, scriptEntry{
			Path:       p,
			ContentB64: base64.StdEncoding.EncodeToString(data),
			Size:       len(data),
		})
	}
	return entries, nil
}

// materializeScripts writes a bundle's inlined scripts into the target crew's
// shared dir via the same `/files/save` path `crewship apply` uses. Idempotent:
// an identical existing file is skipped. Collision-safe: a dest that exists
// with DIFFERENT content fails loudly unless force is set — never a silent
// overwrite (a routine import must not clobber another routine's script).
func materializeScripts(io crewFileIO, targetCrewID string, scripts []scriptEntry, force bool) error {
	for _, s := range scripts {
		data, err := base64.StdEncoding.DecodeString(s.ContentB64)
		if err != nil {
			return fmt.Errorf("script %q: undecodable content_b64: %w", s.Path, err)
		}
		crewPath, err := scriptCrewFilePath(s.Path)
		if err != nil {
			return fmt.Errorf("script %q: %w", s.Path, err)
		}
		existing, ok, err := io.download(targetCrewID, crewPath)
		if err != nil {
			return fmt.Errorf("script %q: check existing in target crew: %w", s.Path, err)
		}
		if ok {
			if bytes.Equal(existing, data) {
				continue // idempotent: already materialized, identical
			}
			if !force {
				return fmt.Errorf("script %q already exists in target crew at %s with different content — "+
					"pass --force to overwrite (a crew script is shared across routines; overwriting may break another routine)", s.Path, crewPath)
			}
		}
		if err := io.save(targetCrewID, crewPath, data); err != nil {
			return fmt.Errorf("script %q: save into target crew: %w", s.Path, err)
		}
	}
	return nil
}
