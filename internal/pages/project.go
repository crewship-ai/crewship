package pages

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// SourceProject is the portable source section of a Page bundle v2.
// It has no credentials, grants, publication state or runtime snapshots.
// Reading it never writes files, installs dependencies or executes code.
// This type does not enable custom rendering or change the existing v1 importer.
type SourceProject struct {
	Format  string        `json:"format" yaml:"format"`
	Runtime string        `json:"runtime" yaml:"runtime"`
	Files   []ProjectFile `json:"files" yaml:"files"`
}

type ProjectFile struct {
	Path     string `json:"path" yaml:"path"`
	Encoding string `json:"encoding" yaml:"encoding"`
	Content  string `json:"content" yaml:"content"`
}

const (
	SourceProjectFormat     = "crewship-page-source/v1"
	SourceProjectRuntime    = "react-vite-typescript/v1"
	MaxProjectDocumentBytes = 4 << 20
	MaxProjectSourceBytes   = 2 << 20
	MaxProjectFileBytes     = 512 << 10
	MaxProjectFiles         = 256
)

// Validate enforces transport limits and portable relative file names. It is
// not a secret scanner or a proof that the source is safe to execute: builds
// still require an isolated worker and secrets must never enter its environment.
func (p *SourceProject) Validate() error {
	if p == nil || p.Format != SourceProjectFormat {
		return errors.New("unsupported Page source format")
	}
	if p.Runtime != SourceProjectRuntime {
		return fmt.Errorf("unsupported Page runtime %q", p.Runtime)
	}
	if len(p.Files) == 0 || len(p.Files) > MaxProjectFiles {
		return fmt.Errorf("project must contain 1..%d files", MaxProjectFiles)
	}
	seen := make(map[string]bool, len(p.Files))
	total := 0
	for _, f := range p.Files {
		if err := validateProjectPath(f.Path); err != nil {
			return err
		}
		// Reject case collisions even on Linux so export remains portable.
		key := strings.ToLower(f.Path)
		if seen[key] {
			return fmt.Errorf("duplicate or case-colliding project path %q", f.Path)
		}
		seen[key] = true
		data, err := f.Bytes()
		if err != nil {
			return fmt.Errorf("file %q: %w", f.Path, err)
		}
		total += len(data)
		if total > MaxProjectSourceBytes {
			return fmt.Errorf("project exceeds %d decoded bytes", MaxProjectSourceBytes)
		}
	}
	for name := range seen {
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if seen[dir] {
				return fmt.Errorf("project path %q conflicts with file %q", name, dir)
			}
		}
	}
	for _, required := range []string{"package.json", "pnpm-lock.yaml", "index.html"} {
		found := false
		for _, f := range p.Files {
			if f.Path == required {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("project requires %s", required)
		}
	}
	return nil
}

func validateProjectPath(name string) error {
	if name == "" || len(name) > 240 || !utf8.ValidString(name) ||
		path.IsAbs(name) || path.Clean(name) != name || name == "." {
		return fmt.Errorf("invalid project path %q", name)
	}
	for _, c := range name {
		if unicode.IsControl(c) || strings.ContainsRune(`\:<>"|?*`, c) {
			return fmt.Errorf("non-portable project path %q", name)
		}
	}
	for _, part := range strings.Split(strings.ToLower(name), "/") {
		if part == ".." || strings.TrimSpace(part) != part || strings.HasSuffix(part, ".") {
			return fmt.Errorf("invalid project path %q", name)
		}
		switch part {
		case ".git", "node_modules", "dist", ".ssh", ".aws", ".npmrc", ".pnpmrc", ".netrc", ".git-credentials":
			return fmt.Errorf("excluded project path %q", name)
		}
		if part == ".env" || strings.HasPrefix(part, ".env.") {
			return fmt.Errorf("environment files cannot be exported: %q", name)
		}
		base := strings.SplitN(part, ".", 2)[0]
		if base == "con" || base == "prn" || base == "aux" || base == "nul" ||
			(len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) && base[3] >= '1' && base[3] <= '9') {
			return fmt.Errorf("reserved project path %q", name)
		}
	}
	return nil
}

// Bytes returns one bounded file. Base64 must be canonical; accepting whitespace
// or alternate encodings would make two source documents mean the same bytes.
func (f ProjectFile) Bytes() ([]byte, error) {
	switch f.Encoding {
	case "utf8":
		if len(f.Content) > MaxProjectFileBytes || !utf8.ValidString(f.Content) || strings.ContainsRune(f.Content, 0) {
			return nil, errors.New("invalid or oversized UTF-8 file")
		}
		return []byte(f.Content), nil
	case "base64":
		if len(f.Content) > base64.StdEncoding.EncodedLen(MaxProjectFileBytes) {
			return nil, errors.New("oversized base64 file")
		}
		data, err := base64.StdEncoding.Strict().DecodeString(f.Content)
		if err != nil || len(data) > MaxProjectFileBytes || base64.StdEncoding.EncodeToString(data) != f.Content {
			return nil, errors.New("invalid or oversized canonical base64 file")
		}
		return data, nil
	default:
		return nil, fmt.Errorf("unsupported file encoding %q", f.Encoding)
	}
}

// ParseSourceProject reads exactly one strict YAML (or JSON) document.
func ParseSourceProject(r io.Reader) (*SourceProject, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxProjectDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxProjectDocumentBytes {
		return nil, errors.New("Page source document is too large")
	}
	// Inspect nodes before typed decoding: never expand aliases from an import.
	var node yaml.Node
	if err := yaml.Unmarshal(b, &node); err != nil {
		return nil, fmt.Errorf("parse Page source: %w", err)
	}
	if err := validateProjectNode(&node, 0); err != nil {
		return nil, err
	}
	d := yaml.NewDecoder(bytes.NewReader(b))
	d.KnownFields(true)
	var p SourceProject
	if err := d.Decode(&p); err != nil {
		return nil, fmt.Errorf("decode Page source: %w", err)
	}
	var extra yaml.Node
	if err := d.Decode(&extra); err != io.EOF {
		return nil, errors.New("Page source must contain exactly one YAML document")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func validateProjectNode(n *yaml.Node, depth int) error {
	if depth > 16 || n.Kind == yaml.AliasNode || n.Anchor != "" || n.ShortTag() == "!!merge" {
		return errors.New("Page source does not support YAML anchors, aliases, merges or deep nesting")
	}
	for _, child := range n.Content {
		if err := validateProjectNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// MarshalYAMLSource produces deterministic YAML without changing the caller's
// file ordering. The encoded bound applies to generated exports as well as imports.
func (p *SourceProject) MarshalYAMLSource() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	copy := *p
	copy.Files = append([]ProjectFile(nil), p.Files...)
	sort.Slice(copy.Files, func(i, j int) bool { return copy.Files[i].Path < copy.Files[j].Path })
	b, err := yaml.Marshal(copy)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxProjectDocumentBytes {
		return nil, errors.New("encoded Page source document is too large")
	}
	// API JSON escapes HTML-sensitive characters. A source that fits YAML must
	// also fit the wire budget, otherwise a saved draft cannot be downloaded.
	wire, err := json.Marshal(copy)
	if err != nil {
		return nil, err
	}
	if len(wire) > MaxProjectDocumentBytes {
		return nil, errors.New("JSON-encoded Page source document is too large; encode large text files as base64")
	}
	return b, nil
}

// Digest identifies source bytes and runtime, independent of file ordering and
// text/base64 representation. It is not a signature or a build attestation.
func (p *SourceProject) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	files := make(map[string][]byte, len(p.Files))
	for _, f := range p.Files {
		b, err := f.Bytes()
		if err != nil {
			return "", err
		}
		files[f.Path] = b
	}
	b, err := json.Marshal(struct {
		Format  string            `json:"format"`
		Runtime string            `json:"runtime"`
		Files   map[string][]byte `json:"files"`
	}{p.Format, p.Runtime, files})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}
