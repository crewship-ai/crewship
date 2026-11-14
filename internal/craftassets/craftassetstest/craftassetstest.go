// Package craftassetstest builds small, valid Word and Sheet packages for
// tests: a hand-assembled WASI engine that prints its version, and an editor
// bundle the editor origin accepts. Only tests import it.
package craftassetstest

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/klauspost/compress/zstd"

	"github.com/crewship-ai/crewship/internal/craftassets"
)

func uleb(v uint32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func section(id byte, body ...[]byte) []byte {
	content := bytes.Join(body, nil)
	return append(append([]byte{id}, uleb(uint32(len(content)))...), content...)
}

func name(s string) []byte { return append(uleb(uint32(len(s))), s...) }

// VersionEngine is a WASI module whose _start writes text to stdout,
// whatever its arguments: enough to answer `--version`.
func VersionEngine(text string) []byte {
	types := section(1, []byte{2},
		[]byte{0x60, 4, 0x7f, 0x7f, 0x7f, 0x7f, 1, 0x7f}, // fd_write
		[]byte{0x60, 0, 0}) // _start
	imports := section(2, []byte{1}, name("wasi_snapshot_preview1"), name("fd_write"), []byte{0, 0})
	funcs := section(3, []byte{1, 1})
	memory := section(5, []byte{1, 0, 1})
	exports := section(7, []byte{2}, name("memory"), []byte{2, 0}, name("_start"), []byte{0, 1})
	// fd_write(1, iovec at 0, 1 iovec, nwritten at 8); drop
	code := []byte{0x41, 1, 0x41, 0, 0x41, 1, 0x41, 8, 0x10, 0, 0x1a}
	body := append([]byte{0}, append(code, 0x0b)...)
	codeSec := section(10, []byte{1}, uleb(uint32(len(body))), body)
	n := uint32(len(text))
	data := append([]byte{16, 0, 0, 0, byte(n), byte(n >> 8), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, text...)
	dataSec := section(11, []byte{1, 0, 0x41, 0, 0x0b}, uleb(uint32(len(data))), data)
	return bytes.Join([][]byte{{0, 'a', 's', 'm', 1, 0, 0, 0}, types, imports, funcs, memory, exports, codeSec, dataSec}, nil)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Package is one built test package.
type Package struct {
	Archive []byte
	Pin     craftassets.Pin
	// Editor is the editor bundle's digest; EngineSHA256 the engine's.
	Editor       string
	EngineSHA256 string
}

// Build returns a valid package of application whose engine prints
// "<application>-cli <version>" and whose editor shows label.
func Build(application, version, label string) Package {
	enc, _ := zstd.NewWriter(nil)
	defer enc.Close()
	files := map[string][]byte{}
	add := func(path string, raw []byte) craftassets.File {
		files[path] = enc.EncodeAll(raw, nil)
		return craftassets.File{Path: path, SHA256: digest(raw), Size: int64(len(raw))}
	}
	editor := map[string][]byte{
		"index.html": []byte(`<!doctype html><html lang="en"><head><meta charset="utf-8"><title>` + label +
			`</title><script type="module" src="editor.js"></script></head><body><main><h1>` + label + `</h1></main></body></html>`),
		"editor.js": []byte("console.log(" + `"` + label + `"` + ")"),
	}
	hashes := map[string]string{}
	for n, b := range editor {
		hashes[n] = digest(b)
	}
	editorManifest, _ := json.Marshal(map[string]any{"toolchain": "craftassetstest", "source_commit": strings.Repeat("a", 40), "files": hashes})
	editor["manifest.json"] = editorManifest
	editorDigest := digest(editorManifest)
	names := make([]string, 0, len(editor))
	for n := range editor {
		names = append(names, n)
	}
	sort.Strings(names)
	var editorFiles []craftassets.File
	for _, n := range names {
		editorFiles = append(editorFiles, add(application+"/editor/"+editorDigest+"/"+n+".zst", editor[n]))
	}
	rev := strings.Repeat("a", 40)
	engine := VersionEngine(application + "-cli " + version + "\n")
	m := craftassets.Manifest{SchemaVersion: craftassets.SchemaVersion, Toolchain: "craftassetstest", Component: craftassets.Component{
		Application: application, Version: version, EngineRevision: rev,
		Source:   craftassets.Source{Repository: "https://example.invalid/" + application, Commit: rev},
		Engine:   add(application+"/engine.wasm.zst", engine),
		Editor:   craftassets.Editor{Digest: editorDigest, Files: editorFiles},
		Licenses: []craftassets.License{{Name: label, SPDX: "MIT"}},
		Notices:  []craftassets.File{add(application+"/licenses/LICENSE-MIT.zst", []byte("MIT"))},
	}}
	raw, _ := json.Marshal(m)
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	write := func(n string, data []byte) {
		_ = w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg})
		_, _ = w.Write(data)
	}
	write("manifest.json", raw)
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		write(p, files[p])
	}
	_ = w.Close()
	return Package{Archive: b.Bytes(), Pin: craftassets.Pin{Application: application, Version: version, ManifestSHA256: digest(raw)},
		Editor: editorDigest, EngineSHA256: digest(engine)}
}
