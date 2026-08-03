package memport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/safepath"
)

// manifestName is the bundle descriptor written beside the documents.
// OKF is "a directory of markdown files"; the manifest is what lets a
// reader tell an intentional bundle from a folder that happens to hold
// markdown, and records which Crewship scope produced it.
const manifestName = "okf.yaml"

// Manifest is the bundle descriptor. Field order is fixed by the struct
// so two exports of the same memory are byte-identical.
type Manifest struct {
	Format    string          `yaml:"format"`
	Version   string          `yaml:"version"`
	Generator string          `yaml:"generator"`
	Scope     string          `yaml:"scope,omitempty"`
	Documents []ManifestEntry `yaml:"documents"`
}

// ManifestEntry describes one exported document.
type ManifestEntry struct {
	Path    string   `yaml:"path"`
	Tier    string   `yaml:"tier"`
	Title   string   `yaml:"title,omitempty"`
	Tags    []string `yaml:"tags,omitempty"`
	Sources []string `yaml:"sources,omitempty"`
}

// ExportOKF writes docs into dir as an OKF bundle: one markdown file per
// document with a YAML header, plus a manifest.
//
// The exported layout mirrors the Crewship paths rather than flattening
// to okf-style slugs. A bundle that reads like the memory it came from
// is one an operator can diff against the live tree; a re-slugged one
// is not, and we lose nothing by keeping the names.
func ExportOKF(dir string, docs []Doc) error {
	if dir == "" {
		return fmt.Errorf("memport: export needs a destination directory")
	}
	man := Manifest{
		Format:    "okf",
		Version:   "0.1",
		Generator: "crewship",
		Documents: make([]ManifestEntry, 0, len(docs)),
	}

	for _, d := range docs {
		target, err := safepath.JoinRel(dir, filepath.FromSlash(d.RelPath))
		if err != nil {
			return fmt.Errorf("memport: export %s: %w", d.RelPath, err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("memport: export %s: %w", d.RelPath, err)
		}
		out, err := renderFrontmatter(frontmatter{
			Type:  string(d.Tier),
			Title: d.Title,
			Tags:  d.Tags,
			// Recording the origin path is what makes re-import exact
			// rather than a second round of heuristics.
			CrewshipPath: d.RelPath,
		}, d.Body)
		if err != nil {
			return fmt.Errorf("memport: render %s: %w", d.RelPath, err)
		}
		if err := os.WriteFile(target, out, 0o644); err != nil {
			return fmt.Errorf("memport: write %s: %w", d.RelPath, err)
		}
		man.Documents = append(man.Documents, ManifestEntry{
			Path:    d.RelPath,
			Tier:    string(d.Tier),
			Title:   d.Title,
			Tags:    d.Tags,
			Sources: d.Sources,
		})
	}

	buf, err := yaml.Marshal(man)
	if err != nil {
		return fmt.Errorf("memport: render manifest: %w", err)
	}
	manPath, err := safepath.JoinRel(dir, manifestName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(manPath, buf, 0o644); err != nil {
		return fmt.Errorf("memport: write manifest: %w", err)
	}
	return nil
}

// Rejection is one document the memory writer refused. It is not an
// error: a capped or secret-bearing document is a decision the writer
// made on purpose, and the operator needs to see which documents it
// covered.
type Rejection struct {
	RelPath string
	Kind    string
	Detail  map[string]any
}

// ApplyResult is the outcome of an import.
type ApplyResult struct {
	Written  []string
	Rejected []Rejection
}

// defaultImportCap bounds a document whose path matches no canonical
// rule — the consolidator's per-crew <slug>/topics/ tree is the real
// example, and refusing those outright would break round-tripping a
// live crew's memory.
//
// The value is the widest ceiling any recognised memory file has
// (daily logs). An unrecognised path gets the most generous rule we
// apply to anything, never no rule at all: unbounded is how an import
// becomes a way to put a megabyte into a prompt.
const defaultImportCap = 30000

// Apply writes docs into the .memory directory rooted at root.
//
// Every write goes through memory.WriteFile — the caps, the scrubber,
// the flock and the atomic replace are not reimplemented here, because
// an importer that wrote files directly would be a second door into the
// memory tree with none of those guarantees.
//
// cfg carries the caller's policy (scrubber, verifier). Its MaxBytes is
// an OVERRIDE: left at zero, each document is capped by
// memory.CapForPath, so an import lands under the same ceilings the
// agent's own writes live under. Setting it applies one tighter cap to
// everything, which is what the tests and a deliberate `--max-bytes`
// use.
//
// Paths are validated up front, all of them, before the first byte is
// written: a traversal in document seventeen must not leave sixteen
// documents applied.
func Apply(ctx context.Context, root string, docs []Doc, cfg memory.WriteConfig) (ApplyResult, error) {
	if root == "" {
		return ApplyResult{}, fmt.Errorf("memport: import needs a target directory")
	}
	targets := make([]string, len(docs))
	for i, d := range docs {
		t, err := safepath.JoinRel(root, filepath.FromSlash(d.RelPath))
		if err != nil {
			return ApplyResult{}, fmt.Errorf("memport: refusing %q: %w", d.RelPath, err)
		}
		if t == filepath.Clean(root) {
			return ApplyResult{}, fmt.Errorf("memport: refusing %q: resolves to the memory root itself", d.RelPath)
		}
		targets[i] = t
	}

	var res ApplyResult
	for i, d := range docs {
		docCfg := cfg
		if docCfg.MaxBytes == 0 {
			docCfg.MaxBytes = capForImport(d.RelPath)
		}
		wr, err := memory.WriteFile(ctx, targets[i], d.Body, docCfg)
		if err != nil {
			return res, fmt.Errorf("memport: write %s: %w", d.RelPath, err)
		}
		if wr.Rejected {
			res.Rejected = append(res.Rejected, Rejection{
				RelPath: d.RelPath,
				Kind:    wr.RejectionKind,
				Detail:  wr.RejectionDetail,
			})
			continue
		}
		res.Written = append(res.Written, d.RelPath)
	}
	return res, nil
}

// capForImport resolves the ceiling one document is written under.
//
// memory.CapForPath answers for the canonical files and distinguishes
// "recognised, deliberately uncapped" (lessons/learned) from "no rule
// here" — only the second gets the fallback. Collapsing the two would
// quietly put a cap on a file the memory engine chose to leave open.
func capForImport(rel string) int {
	c, known := memory.CapForPath(rel)
	if !known {
		return defaultImportCap
	}
	return c
}
