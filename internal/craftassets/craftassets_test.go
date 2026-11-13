package craftassets

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func compress(t *testing.T, b []byte) []byte {
	t.Helper()
	e, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	return e.EncodeAll(b, nil)
}

// pkg is a Word package under construction.
type pkg struct {
	files    map[string][]byte // compressed, by path
	manifest Manifest
	extra    []*tar.Header // additional entries, written as given
}

const (
	engineBytes = "\x00asm engine bytes"
	indexBytes  = "<!doctype html>"
	noticeBytes = "MIT license text"
)

func newPkg(t *testing.T) *pkg {
	t.Helper()
	p := &pkg{files: map[string][]byte{}}
	add := func(path string, raw string) File {
		p.files[path] = compress(t, []byte(raw))
		return File{Path: path, SHA256: digest([]byte(raw)), Size: int64(len(raw))}
	}
	rev := strings.Repeat("a", 40)
	editor := strings.Repeat("e", 64)
	p.manifest = Manifest{SchemaVersion: SchemaVersion, Toolchain: "rustc 1.99.1", Component: Component{
		Application: "wordcraft", Version: "0.1.0+" + rev[:12], EngineRevision: rev,
		Source:   Source{Repository: "https://example.test/wordcraft", Commit: rev},
		Engine:   add("wordcraft/engine.wasm.zst", engineBytes),
		Editor:   Editor{Digest: editor, Files: []File{add("wordcraft/editor/"+editor+"/index.html.zst", indexBytes)}},
		Licenses: []License{{Name: "WordCraft", SPDX: "MIT"}},
		Notices:  []File{add("wordcraft/licenses/LICENSE-MIT.zst", noticeBytes)},
	}}
	return p
}

func (p *pkg) manifestBytes(*testing.T) []byte {
	raw, err := json.Marshal(p.manifest)
	if err != nil {
		panic(err)
	}
	return raw
}

func (p *pkg) pin(t *testing.T) Pin {
	return Pin{Application: "wordcraft", Version: p.manifest.Component.Version, ManifestSHA256: digest(p.manifestBytes(t))}
}

func (p *pkg) tar(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	write := func(name string, data []byte) {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	write(manifestName, p.manifestBytes(t))
	for name, data := range p.files {
		write(name, data)
	}
	for _, h := range p.extra {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := w.Write(make([]byte, h.Size)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInstallVerifiesAndServesAPackage(t *testing.T) {
	p := newPkg(t)
	store := Store{Root: t.TempDir()}
	pin := p.pin(t)
	if _, err := store.Open(pin); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("before install: %v", err)
	}
	inst, err := store.Install(pin, bytes.NewReader(p.tar(t)))
	if err != nil {
		t.Fatal(err)
	}
	if inst.Dir != filepath.Join(store.Root, "wordcraft", pin.ManifestSHA256) || inst.Component.Version != pin.Version {
		t.Fatalf("installed = %+v", inst)
	}
	if b, err := inst.EngineLoader()(); err != nil || string(b) != engineBytes {
		t.Fatalf("engine %q %v", b, err)
	}
	if b, err := inst.Notice("LICENSE-MIT"); err != nil || string(b) != noticeBytes {
		t.Fatalf("notice %q %v", b, err)
	}
	if _, err := inst.Notice("../manifest.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("notice outside licenses: %v", err)
	}
	editor := inst.EditorPackages()
	if b, err := fs.ReadFile(editor, strings.Repeat("e", 64)+"/index.html"); err != nil || string(b) != indexBytes {
		t.Fatalf("editor %q %v", b, err)
	}
	if entries, err := fs.ReadDir(editor, "."); err != nil || len(entries) != 1 || entries[0].Name() != strings.Repeat("e", 64) {
		t.Fatalf("editor root %v %v", entries, err)
	}
	// Reopening (a restart) needs no package.
	again, err := store.Open(pin)
	if err != nil || again.Component.Editor.Digest != inst.Component.Editor.Digest {
		t.Fatalf("reopen %+v %v", again, err)
	}
	// Installing again is a no-op and leaves no staging behind.
	if _, err := store.Install(pin, bytes.NewReader(p.tar(t))); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(store.Root)
	if len(entries) != 1 || entries[0].Name() != "wordcraft" {
		t.Fatalf("store root holds %v", entries)
	}
}

func TestInstallRefusesAnythingButThePinnedPackage(t *testing.T) {
	cases := map[string]func(p *pkg) Pin{
		"another manifest": func(p *pkg) Pin { pin := p.pin(nil); pin.ManifestSHA256 = strings.Repeat("0", 64); return pin },
		"another version":  func(p *pkg) Pin { pin := p.pin(nil); pin.Version = "0.2.0"; return pin },
		"tampered engine": func(p *pkg) Pin {
			p.files["wordcraft/engine.wasm.zst"] = p.files["wordcraft/licenses/LICENSE-MIT.zst"]
			return p.pin(nil)
		},
		"missing file": func(p *pkg) Pin { delete(p.files, "wordcraft/licenses/LICENSE-MIT.zst"); return p.pin(nil) },
		"unlisted file": func(p *pkg) Pin {
			p.extra = append(p.extra, &tar.Header{Name: "wordcraft/run.sh", Mode: 0o755, Size: 3, Typeflag: tar.TypeReg})
			return p.pin(nil)
		},
		"symlink": func(p *pkg) Pin {
			p.extra = append(p.extra, &tar.Header{Name: "wordcraft/link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
			return p.pin(nil)
		},
		"escaping name": func(p *pkg) Pin {
			p.extra = append(p.extra, &tar.Header{Name: "../outside", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			return p.pin(nil)
		},
		"absolute name": func(p *pkg) Pin {
			p.extra = append(p.extra, &tar.Header{Name: "/tmp/x", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			return p.pin(nil)
		},
		"repeated name": func(p *pkg) Pin {
			p.extra = append(p.extra, &tar.Header{Name: manifestName, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg})
			return p.pin(nil)
		},
		"engine outside its application": func(p *pkg) Pin {
			f := p.manifest.Component.Engine
			p.files["gridcraft/engine.wasm.zst"] = p.files[f.Path]
			delete(p.files, f.Path)
			f.Path = "gridcraft/engine.wasm.zst"
			p.manifest.Component.Engine = f
			return p.pin(nil)
		},
		"unknown manifest field": func(p *pkg) Pin { p.manifest.Toolchain = "x"; return p.pin(nil) },
		"old schema":             func(p *pkg) Pin { p.manifest.SchemaVersion = 1; return p.pin(nil) },
		"no provenance":          func(p *pkg) Pin { p.manifest.Component.Source.Commit = strings.Repeat("b", 40); return p.pin(nil) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := newPkg(t)
			pin := mutate(p)
			raw := p.tar(t)
			if name == "unknown manifest field" {
				m := bytes.Replace(p.manifestBytes(t), []byte(`"toolchain"`), []byte(`"extra":1,"toolchain"`), 1)
				pin.ManifestSHA256 = digest(m)
				raw = rewriteManifest(t, raw, m)
			}
			store := Store{Root: t.TempDir()}
			if _, err := store.Install(pin, bytes.NewReader(raw)); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("installed: %v", err)
			}
			if _, err := store.Open(pin); !errors.Is(err, ErrNotInstalled) {
				t.Fatalf("visible after a refused install: %v", err)
			}
			if _, err := os.Lstat(filepath.Join(filepath.Dir(store.Root), "outside")); err == nil {
				t.Fatal("wrote outside the store")
			}
			entries, _ := os.ReadDir(store.Root)
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".staging") {
					t.Fatalf("staging left behind: %s", e.Name())
				}
			}
		})
	}
}

func rewriteManifest(t *testing.T, archive, manifest []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	r := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		data, _ := io.ReadAll(r)
		if h.Name == manifestName {
			data = manifest
			h.Size = int64(len(data))
		}
		_ = w.WriteHeader(h)
		_, _ = w.Write(data)
	}
	_ = w.Close()
	return b.Bytes()
}

func TestTamperingAfterInstallIsDetectedOnRead(t *testing.T) {
	p := newPkg(t)
	store := Store{Root: t.TempDir()}
	inst, err := store.Install(p.pin(t), bytes.NewReader(p.tar(t)))
	if err != nil {
		t.Fatal(err)
	}
	engine := filepath.Join(inst.Dir, "wordcraft", "engine.wasm.zst")
	_ = os.Chmod(engine, 0o640)
	if err = os.WriteFile(engine, compress(t, []byte("\x00asm evil")), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = inst.EngineLoader()(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered engine read: %v", err)
	}
	index := filepath.Join(inst.Dir, "wordcraft", "editor", strings.Repeat("e", 64), "index.html.zst")
	_ = os.Chmod(index, 0o640)
	_ = os.WriteFile(index, compress(t, []byte("<script>evil</script>")), 0o640)
	if _, err = fs.ReadFile(inst.EditorPackages(), strings.Repeat("e", 64)+"/index.html"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered editor read: %v", err)
	}
	// A damaged copy is replaced by a fresh install.
	if _, err = store.Install(p.pin(t), bytes.NewReader(p.tar(t))); err != nil {
		t.Fatal(err)
	}
	if b, err := inst.EngineLoader()(); err != nil || string(b) != engineBytes {
		t.Fatalf("after reinstall %q %v", b, err)
	}
	// A manifest edited in place no longer matches the pin.
	m := filepath.Join(inst.Dir, manifestName)
	_ = os.Chmod(m, 0o640)
	_ = os.WriteFile(m, bytes.Replace(p.manifestBytes(t), []byte("rustc"), []byte("RUSTC"), 1), 0o640)
	if _, err = store.Open(p.pin(t)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("edited manifest: %v", err)
	}
}

func TestDownloadInstallsFromTheCatalogURL(t *testing.T) {
	p := newPkg(t)
	archive := p.tar(t)
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if r.URL.Path == "/v1.2.3/"+AssetName("wordcraft") {
			_, _ = w.Write(archive)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	store := Store{Root: t.TempDir()}
	catalog := Catalog{Pins: map[string]Pin{"wordcraft": p.pin(t)}, BaseURL: srv.URL + "/v1.2.3/"}
	inst, err := store.Download(context.Background(), catalog, "wordcraft", srv.Client())
	if err != nil || asked != "/v1.2.3/crewship-craft-wordcraft.craftpkg" {
		t.Fatalf("download %v (asked %s)", err, asked)
	}
	if b, err := inst.EngineLoader()(); err != nil || string(b) != engineBytes {
		t.Fatalf("engine %q %v", b, err)
	}
	if _, err = store.Download(context.Background(), catalog, "gridcraft", srv.Client()); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("unpinned application: %v", err)
	}
	catalog.Pins["wordcraft"] = Pin{Application: "wordcraft", Version: "9", ManifestSHA256: strings.Repeat("1", 64)}
	if _, err = (Store{Root: t.TempDir()}).Download(context.Background(), catalog, "wordcraft", srv.Client()); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("a package that is not the pinned one: %v", err)
	}
	catalog.BaseURL = srv.URL + "/missing/"
	if _, err = (Store{Root: t.TempDir()}).Download(context.Background(), catalog, "wordcraft", srv.Client()); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("missing asset: %v", err)
	}
	catalog.BaseURL = ""
	if _, err = store.Download(context.Background(), catalog, "wordcraft", srv.Client()); err == nil || !strings.Contains(err.Error(), "from a file") {
		t.Fatalf("no address: %v", err)
	}
}

func TestCatalog(t *testing.T) {
	sha := strings.Repeat("a", 64)
	c, err := ReleaseCatalog("wordcraft=0.11.0+b0fb5cb571b6="+sha+",gridcraft=0.4.0="+sha, "https://github.com/crewship-ai/crewship/releases/download/v1.0.0")
	// Pins named by configuration replace the release catalog; the operator,
	// not the release, then decides what is trusted.
	if err != nil || len(c.Pins) != 2 || c.Pins["wordcraft"].Version != "0.11.0+b0fb5cb571b6" || !c.Operator {
		t.Fatalf("catalog %+v %v", c, err)
	}
	if got := c.URL("gridcraft"); got != "https://github.com/crewship-ai/crewship/releases/download/v1.0.0/crewship-craft-gridcraft.craftpkg" {
		t.Fatalf("URL %s", got)
	}
	if String(c.Pins) != "gridcraft=0.4.0="+sha+",wordcraft=0.11.0+b0fb5cb571b6="+sha {
		t.Fatalf("String %s", String(c.Pins))
	}
	// An ordinary build trusts nothing.
	if c, err := ReleaseCatalog("", ""); err != nil || len(c.Pins) != 0 || c.BaseURL != "" || c.Operator {
		t.Fatalf("default %+v %v", c, err)
	}
	for _, bad := range []string{"powerpoint=1=" + sha, "wordcraft=1=" + sha[:63], "wordcraft=1", "wordcraft==" + sha, "wordcraft=1/2=" + sha, "wordcraft=1=" + sha + ",wordcraft=2=" + sha} {
		if _, err := ParsePins(bad); err == nil {
			t.Errorf("accepted pins %q", bad)
		}
	}
	for _, ok := range []string{"http://127.0.0.1:8080/v1/", "http://localhost/v1/", "https://mirror.example/v1/"} {
		if _, err := ReleaseCatalog("", ok); err != nil {
			t.Errorf("refused URL %q: %v", ok, err)
		}
	}
	for _, bad := range []string{"ftp://x/", "https://user@x/", "https:///x", "https://x/?a=1", "not a url", "http://mirror.example/v1/"} {
		if _, err := ReleaseCatalog("", bad); err == nil {
			t.Errorf("accepted URL %q", bad)
		}
	}
}

func TestInstallEnforcesPackageLimits(t *testing.T) {
	defer func(b int64, f int) { packageBytes, packageFiles = b, f }(packageBytes, packageFiles)
	p := newPkg(t)
	archive := p.tar(t)
	// Four entries (manifest, engine, editor, notice) fit; three do not.
	packageFiles = 3
	if _, err := (Store{Root: t.TempDir()}).Install(p.pin(t), bytes.NewReader(archive)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("more files than allowed: %v", err)
	}
	packageFiles = 4
	if _, err := (Store{Root: t.TempDir()}).Install(p.pin(t), bytes.NewReader(archive)); err != nil {
		t.Fatalf("exactly at the file limit: %v", err)
	}
	packageBytes = int64(len(p.manifestBytes(t))) + 8
	if _, err := (Store{Root: t.TempDir()}).Install(p.pin(t), bytes.NewReader(archive)); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("larger than allowed: %v", err)
	}
}

// A file compressed with a window larger than the build script's 8 MiB is
// refused rather than decoded into a large buffer.
func TestLargeCompressionWindowIsRefused(t *testing.T) {
	// More than one block, so the frame declares its window instead of being
	// a single segment sized by its content.
	raw := bytes.Repeat([]byte("crewship"), 2<<20)
	var b bytes.Buffer
	e, err := zstd.NewWriter(&b, zstd.WithWindowSize(64<<20), zstd.WithSingleSegment(false))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < len(raw); i += 1 << 20 {
		_, _ = e.Write(raw[i : i+1<<20])
	}
	_ = e.Close()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.zst"), b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(os.DirFS(dir), File{Path: "f.zst", SHA256: digest(raw), Size: int64(len(raw))}, 64<<20); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("64 MiB window accepted: %v", err)
	}
}

func TestSweepRemovesOnlyInterruptedInstallations(t *testing.T) {
	p := newPkg(t)
	store := Store{Root: t.TempDir()}
	inst, err := store.Install(p.pin(t), bytes.NewReader(p.tar(t)))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(store.Root, ".download-wordcraft-1"), []byte("partial"), 0o600)
	_ = os.MkdirAll(filepath.Join(store.Root, ".staging-wordcraft-2", "wordcraft"), 0o750)
	if err := store.Sweep(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(store.Root)
	if len(entries) != 1 || entries[0].Name() != "wordcraft" {
		t.Fatalf("after sweep: %v", entries)
	}
	if _, err := store.Open(inst.Pin); err != nil {
		t.Fatalf("sweep touched an installed package: %v", err)
	}
	if err := (Store{Root: filepath.Join(t.TempDir(), "missing")}).Sweep(); err != nil {
		t.Fatalf("sweep of a missing store: %v", err)
	}
}
