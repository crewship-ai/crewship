// Package craftassets installs and reads the Word (WordCraft) and Sheet
// (GridCraft) components of Crewship (#5315): each application's document
// engine as a WASI module and its browser editor bundle, in one package per
// application and version.
//
// Nothing is built into the binary. A release binary carries only a catalog
// of pins (the SHA-256 of each package's manifest.json); Crewship downloads a
// package when an instance administrator turns the application on, or takes
// it from a file (offline). A package is activated only when its manifest
// matches a pin from the catalog, and every file is verified against the
// manifest when it is installed and again whenever it is read.
//
// Installed packages live side by side under the store root as
// <application>/<pin>/, so a newer version never replaces the one existing
// documents were written with.
package craftassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"sync"
	"testing/fstest"

	"github.com/klauspost/compress/zstd"
)

// ErrNotInstalled means the pinned package is not in the store yet.
var ErrNotInstalled = errors.New("Craft application component is not installed")

// ErrNotPublished means this binary trusts no package for the application.
var ErrNotPublished = errors.New("this Crewship build has no Craft application component for the application")

// ErrIntegrity means bytes do not match the pin or the manifest.
var ErrIntegrity = errors.New("Craft application component does not match its pin")

const (
	manifestName = "manifest.json"
	// SchemaVersion is the only package manifest format this binary reads.
	SchemaVersion  = 2
	maxManifest    = 1 << 20
	maxEngineBytes = 256 << 20
	maxEditorBytes = 128 << 20
	maxNoticeBytes = 16 << 20
	// MaxPackageBytes bounds a downloaded or uploaded package.
	MaxPackageBytes = 512 << 20
)

// Limits of one package; variables so tests can lower them.
var (
	packageBytes int64 = MaxPackageBytes
	packageFiles       = 4096
)

var (
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	pathPattern     = regexp.MustCompile(`^[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
)

// Applications are the applications a package may hold.
var Applications = []string{"wordcraft", "gridcraft"}

// Source records where a component was built from.
type Source struct {
	Repository  string `json:"repository"`
	Commit      string `json:"commit"`
	PatchSHA256 string `json:"patch_sha256,omitempty"`
}

// License is one license that applies to the component.
type License struct {
	Name string `json:"name"`
	SPDX string `json:"spdx"`
}

// File is one compressed file of a package.
type File struct {
	Path   string `json:"path"`   // relative to the package root, ends in .zst
	SHA256 string `json:"sha256"` // of the decompressed bytes
	Size   int64  `json:"size"`   // decompressed
}

// Editor is the browser editor bundle: a digest-named package the editor
// origin serves at /_editor/b/<digest>/.
type Editor struct {
	Digest string `json:"digest"` // sha256 of its manifest.json, the editor pin
	Files  []File `json:"files"`  // under <application>/editor/<digest>/
}

// Component is one application in one version.
type Component struct {
	Application    string    `json:"application"`
	Version        string    `json:"version"`
	EngineRevision string    `json:"engine_revision"`
	Source         Source    `json:"source"`
	Engine         File      `json:"engine"`
	Editor         Editor    `json:"editor"`
	Licenses       []License `json:"licenses"`
	// Notices are the license texts and crate inventory, under
	// <application>/licenses/.
	Notices []File `json:"notices"`
}

// Manifest is a package's manifest.json.
type Manifest struct {
	SchemaVersion int       `json:"schema_version"`
	Toolchain     string    `json:"toolchain"`
	Component     Component `json:"component"`
}

// files lists every compressed file the manifest names.
func (m Manifest) files() []File {
	c := m.Component
	out := append([]File{c.Engine}, c.Editor.Files...)
	return append(out, c.Notices...)
}

func parseManifest(raw []byte, pin Pin) (Manifest, error) {
	if len(raw) > maxManifest {
		return Manifest{}, ErrIntegrity
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != pin.ManifestSHA256 {
		return Manifest{}, fmt.Errorf("%w: manifest is not the pinned one", ErrIntegrity)
	}
	var m Manifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil || m.SchemaVersion != SchemaVersion {
		return Manifest{}, fmt.Errorf("%w: unsupported manifest", ErrIntegrity)
	}
	c := m.Component
	if c.Application != pin.Application || c.Version != pin.Version {
		return Manifest{}, fmt.Errorf("%w: package is not %s %s", ErrIntegrity, pin.Application, pin.Version)
	}
	if !revisionPattern.MatchString(c.EngineRevision) || c.Source.Commit != c.EngineRevision || len(c.Licenses) == 0 {
		return Manifest{}, fmt.Errorf("%w: %s: incomplete provenance", ErrIntegrity, c.Application)
	}
	if !validFile(c.Engine, c.Application+"/") || !sha256Pattern.MatchString(c.Editor.Digest) || len(c.Editor.Files) == 0 {
		return Manifest{}, fmt.Errorf("%w: %s: invalid file entries", ErrIntegrity, c.Application)
	}
	if c.Engine.Size > maxEngineBytes {
		return Manifest{}, fmt.Errorf("%w: %s: engine too large", ErrIntegrity, c.Application)
	}
	var editor int64
	for _, f := range c.Editor.Files {
		editor += f.Size
		if !validFile(f, c.Application+"/editor/"+c.Editor.Digest+"/") || editor > maxEditorBytes {
			return Manifest{}, fmt.Errorf("%w: %s: invalid editor file", ErrIntegrity, c.Application)
		}
	}
	for _, f := range c.Notices {
		if !validFile(f, c.Application+"/licenses/") || f.Size > maxNoticeBytes {
			return Manifest{}, fmt.Errorf("%w: %s: invalid notice file", ErrIntegrity, c.Application)
		}
	}
	seen := map[string]bool{}
	for _, f := range m.files() {
		if seen[f.Path] {
			return Manifest{}, fmt.Errorf("%w: %s: repeated file", ErrIntegrity, c.Application)
		}
		seen[f.Path] = true
	}
	return m, nil
}

func validFile(f File, prefix string) bool {
	return pathPattern.MatchString(f.Path) && fs.ValidPath(f.Path) && len(f.Path) > len(prefix)+4 && f.Path[:len(prefix)] == prefix &&
		path.Ext(f.Path) == ".zst" && sha256Pattern.MatchString(f.SHA256) && f.Size > 0
}

var decoderPool = sync.Pool{New: func() any {
	// The build script compresses with the default 8 MiB window; a larger one
	// in a package is refused instead of allocating up to 512 MiB for it.
	d, _ := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(8<<20), zstd.WithDecoderLowmem(true))
	return d
}}

// verified decompresses one file, bounded by its declared size, and checks its
// digest; it writes the bytes to w as they are verified, so a caller that
// only checks the file keeps none of them.
func verified(source fs.FS, f File, w io.Writer) error {
	compressed, err := source.Open(f.Path)
	if err != nil {
		return ErrIntegrity
	}
	defer compressed.Close()
	d := decoderPool.Get().(*zstd.Decoder)
	defer decoderPool.Put(d)
	if err = d.Reset(compressed); err != nil {
		return ErrIntegrity
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(h, w), io.LimitReader(d, f.Size+1))
	if err != nil || n != f.Size || hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return ErrIntegrity
	}
	return nil
}

// read decompresses and verifies one file.
func read(source fs.FS, f File, limit int64) ([]byte, error) {
	if f.Size > limit {
		return nil, ErrIntegrity
	}
	var b bytes.Buffer
	b.Grow(int(f.Size))
	if err := verified(source, f, &b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Installed is one verified package in the store.
type Installed struct {
	Pin       Pin
	Toolchain string
	Component Component
	Dir       string
	files     fs.FS
}

// EngineLoader returns a function that reads and verifies the WASI module, so
// the module is decompressed only when the engine is first compiled.
func (i *Installed) EngineLoader() func() ([]byte, error) {
	files, engine := i.files, i.Component.Engine
	return func() ([]byte, error) { return read(files, engine, maxEngineBytes) }
}

// Notice returns one verified license notice.
func (i *Installed) Notice(name string) ([]byte, error) {
	for _, f := range i.Component.Notices {
		if f.Path == i.Component.Application+"/licenses/"+name+".zst" {
			return read(i.files, f, maxNoticeBytes)
		}
	}
	return nil, fs.ErrNotExist
}

// EditorPackages returns the editor bundle as a digest-named directory, the
// layout editororigin takes. Each file is decompressed and verified only when
// it is opened; the editor origin verifies the bundle again against its pin.
func (i *Installed) EditorPackages() fs.FS {
	c := i.Component
	p := &lazyPackages{source: i.files, index: fstest.MapFS{}, files: map[string]File{}}
	prefix := c.Application + "/editor/" + c.Editor.Digest + "/"
	for _, f := range c.Editor.Files {
		name := c.Editor.Digest + "/" + f.Path[len(prefix):len(f.Path)-len(".zst")]
		p.index[name] = &fstest.MapFile{Mode: 0o444}
		p.files[name] = f
	}
	return p
}

type lazyPackages struct {
	source fs.FS
	index  fstest.MapFS
	files  map[string]File
}

func (p *lazyPackages) Open(name string) (fs.File, error) {
	f, ok := p.files[name]
	if !ok {
		return p.index.Open(name)
	}
	b, err := read(p.source, f, maxEditorBytes)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: err}
	}
	return fstest.MapFS{name: &fstest.MapFile{Data: b, Mode: 0o444}}.Open(name)
}

func (p *lazyPackages) ReadDir(name string) ([]fs.DirEntry, error) { return fs.ReadDir(p.index, name) }
