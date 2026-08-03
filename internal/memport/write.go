package memport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	Scope   string   `yaml:"scope,omitempty"`
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
	// Re-exporting into the same directory is the workflow this feature
	// is sold on — "readable, diffable and git-friendly" only means
	// anything if you can refresh a bundle in place and read the diff.
	// So a previous bundle is not refused; it is PRUNED. Documents the
	// last manifest listed and this export does not produce are removed,
	// which is what stops a file deleted from memory since then sitting
	// in the directory looking current.
	//
	// Only files the previous manifest claims are touched. Anything else
	// in the directory — .git, a README, the operator's own notes — is
	// left exactly alone, because this function did not put it there.
	if err := pruneStaleBundle(dir, docs); err != nil {
		return err
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
			Scope: string(d.Scope),
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
			Scope:   string(d.Scope),
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
	Failed   []Failure
}

// Failure is one document that could not be written for a reason that
// is not write policy: a refused path, or the filesystem saying no.
//
// It is per-document and non-fatal by design. Aborting the loop would
// leave the documents already replaced in place while telling the
// caller the whole import failed — the operator would believe nothing
// happened while half their memory was new.
type Failure struct {
	RelPath string
	Reason  string
}

// Apply writes docs into the .memory directory rooted at root.
//
// Every write goes through memory.WriteFile — the caps, the scrubber,
// the flock and the atomic replace are not reimplemented here, because
// an importer that wrote files directly would be a second door into the
// memory tree with none of those guarantees.
//
// # Confinement
//
// safepath.JoinRel confines the path TEXT, and that is not sufficient on
// its own: the agent owns its .memory directory, so it can replace a
// subdirectory with a symlink and every path stays lexically inside the
// root while the bytes land in another crew's tree. So each target also
// has its parents created one segment at a time (EnsureDirNoFollow) and
// is re-checked against the canonicalised root (AssertInsideRoot) — the
// same pair the dispatcher runs before its own writes.
//
// # What may be written
//
// checkImportPath is a closed allowlist, matching every other write door
// in the product. cfg.MaxBytes overrides the per-path ceiling when the
// caller wants one tighter cap for everything.
func Apply(ctx context.Context, root string, docs []Doc, cfg memory.WriteConfig) (ApplyResult, error) {
	if root == "" {
		return ApplyResult{}, fmt.Errorf("memport: import needs a target directory")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return ApplyResult{}, fmt.Errorf("memport: prepare memory root: %w", err)
	}

	var res ApplyResult
	for _, d := range docs {
		maxBytes, refusal := checkImportPath(d.RelPath, d.Scope)
		if refusal != "" {
			res.Failed = append(res.Failed, Failure{RelPath: d.RelPath, Reason: string(refusal)})
			continue
		}
		target, err := safepath.JoinRel(root, filepath.FromSlash(d.RelPath))
		if err != nil || target == filepath.Clean(root) {
			res.Failed = append(res.Failed, Failure{
				RelPath: d.RelPath,
				Reason:  "refused: path does not stay inside the target memory directory",
			})
			continue
		}
		if err := memory.EnsureDirNoFollow(root, filepath.Dir(target)); err != nil {
			res.Failed = append(res.Failed, Failure{RelPath: d.RelPath, Reason: confinementReason(err)})
			continue
		}
		if err := memory.AssertInsideRoot(root, target); err != nil {
			res.Failed = append(res.Failed, Failure{RelPath: d.RelPath, Reason: confinementReason(err)})
			continue
		}

		docCfg := cfg
		if docCfg.MaxBytes == 0 {
			docCfg.MaxBytes = maxBytes
		}
		wr, err := memory.WriteFile(ctx, target, d.Body, docCfg)
		if err != nil {
			// WriteFile's error text carries the absolute host path. The
			// caller is told WHICH document failed, not where the server
			// keeps it.
			res.Failed = append(res.Failed, Failure{RelPath: d.RelPath, Reason: "write failed on the server"})
			continue
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

// confinementReason renders a containment failure without echoing the
// server's absolute paths back to the caller. What the operator needs is
// "this was refused for where it points", not the storage layout.
func confinementReason(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(err.Error(), "symlink") {
		return "refused: a symlink inside the memory directory redirects this path"
	}
	return "refused: path does not stay inside the target memory directory"
}

// pruneStaleBundle removes documents a previous export wrote that this
// one will not. Absent or unreadable manifest means nothing to prune —
// a directory this function has never written to is not its business.
func pruneStaleBundle(dir string, docs []Doc) error {
	manPath, err := safepath.JoinRel(dir, manifestName)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(manPath)
	if err != nil {
		return nil
	}
	var prev Manifest
	if err := yaml.Unmarshal(raw, &prev); err != nil {
		return nil
	}
	keep := make(map[string]bool, len(docs))
	for _, d := range docs {
		keep[d.RelPath] = true
	}
	for _, e := range prev.Documents {
		if e.Path == "" || keep[e.Path] {
			continue
		}
		stale, err := safepath.JoinRel(dir, filepath.FromSlash(e.Path))
		if err != nil {
			// A manifest naming a path outside its own directory is not
			// something to act on.
			continue
		}
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("memport: removing stale %s from the previous bundle: %w", e.Path, err)
		}
	}
	return nil
}
