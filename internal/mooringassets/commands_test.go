package mooringassets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func commandFixture(t *testing.T) fstest.MapFS {
	t.Helper()
	source := assetFixture(t)
	var m Manifest
	_ = json.Unmarshal(source["embedded/generated/manifest.json"].Data, &m)
	elf := make([]byte, 64)
	copy(elf, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	elf[16] = 3
	elf[18] = 62
	sum := sha256.Sum256(elf)
	digest := hex.EncodeToString(sum[:])
	m.Commands = &CommandsManifest{CommandFormat, CommandEngineRevision, CommandClockPatchSHA256, digest, digest, ""}
	provenance, _ := json.Marshal(map[string]string{"crewship_source": m.SourceSHA, "native_upstream_source": CommandEngineRevision, "clock_patch_sha256": CommandClockPatchSHA256, "format": CommandFormat})
	files := map[string][]byte{"index.html": []byte("<!doctype html>"), "child.js": []byte("child"), "wordcraft_web.js": []byte("native"), "wordcraft_web_bg.wasm": []byte("wasm"), "build-provenance.json": provenance}
	hashes := map[string]string{}
	for name, body := range files {
		s := sha256.Sum256(body)
		hashes[name] = hex.EncodeToString(s[:])
	}
	manifest, _ := json.Marshal(map[string]any{"files": hashes, "source_commit": m.SourceSHA, "toolchain": map[string]string{"rustc": "1.99.1"}})
	h := sha256.Sum256(manifest)
	m.Commands.EditorBundleDigest = hex.EncodeToString(h[:])
	folder := "embedded/generated/editor/" + m.Commands.EditorBundleDigest + "/"
	for name, body := range files {
		source[folder+name] = &fstest.MapFile{Data: body}
	}
	source[folder+"manifest.json"] = &fstest.MapFile{Data: manifest}
	source["embedded/generated/command-host"] = &fstest.MapFile{Data: append([]byte(nil), elf...)}
	source["embedded/generated/command-engine"] = &fstest.MapFile{Data: append([]byte(nil), elf...)}
	raw, _ := json.Marshal(m)
	source["embedded/generated/manifest.json"].Data = raw
	return source
}
func TestCommandsVerifiedImmutableExtractionAndExactCleanup(t *testing.T) {
	source := commandFixture(t)
	parent := t.TempDir()
	unrelated := filepath.Join(parent, "unrelated")
	_ = os.WriteFile(unrelated, []byte("retain"), 0600)
	p, e := prepare(source, parent)
	if e != nil {
		t.Fatal(e)
	}
	owned := []string{p.Path, p.CommandHostPath, p.CommandEnginePath}
	for _, name := range owned {
		info, e := os.Stat(name)
		if e != nil || info.Mode().Perm() != 0500 {
			t.Fatal("not immutable private executable", e)
		}
	}
	if info, _ := os.Stat(p.directory); info.Mode().Perm() != 0700 {
		t.Fatal("directory not private")
	}
	p.Path = unrelated
	p.CommandHostPath = unrelated
	p.CommandEnginePath = unrelated
	if e = p.Close(); e != nil {
		t.Fatal(e)
	}
	for _, name := range owned {
		if _, e = os.Stat(name); !errors.Is(e, fs.ErrNotExist) {
			t.Fatal("owned file retained", e)
		}
	}
	if body, e := os.ReadFile(unrelated); e != nil || string(body) != "retain" {
		t.Fatal("foreign file changed")
	}
	if e = p.Close(); e != nil {
		t.Fatal("not idempotent", e)
	}
}
func TestCommandsCorruptionRefusedBeforeAnyExtraction(t *testing.T) {
	for _, problem := range []string{"host_hash", "engine_hash", "elf_class", "elf_machine", "format", "source", "clock", "bundle_missing", "bundle_file", "bundle_provenance", "unknown_commands", "legacy_collision"} {
		t.Run(problem, func(t *testing.T) {
			source := commandFixture(t)
			var m Manifest
			_ = json.Unmarshal(source["embedded/generated/manifest.json"].Data, &m)
			switch problem {
			case "host_hash":
				m.Commands.HostSHA256 = strings.Repeat("0", 64)
			case "engine_hash":
				m.Commands.EngineSHA256 = strings.Repeat("0", 64)
			case "elf_class":
				source["embedded/generated/command-host"].Data[4] = 1
				hash := sha256.Sum256(source["embedded/generated/command-host"].Data)
				m.Commands.HostSHA256 = hex.EncodeToString(hash[:])
			case "elf_machine":
				source["embedded/generated/command-engine"].Data[18] = 183
				hash := sha256.Sum256(source["embedded/generated/command-engine"].Data)
				m.Commands.EngineSHA256 = hex.EncodeToString(hash[:])
			case "format":
				m.Commands.Format = "other"
			case "source":
				m.Commands.EngineRevision = strings.Repeat("0", 40)
			case "clock":
				m.Commands.ClockPatchSHA256 = strings.Repeat("0", 64)
			case "bundle_missing":
				delete(source, "embedded/generated/editor/"+m.Commands.EditorBundleDigest+"/manifest.json")
			case "bundle_file":
				source["embedded/generated/editor/"+m.Commands.EditorBundleDigest+"/child.js"].Data = []byte("corrupt")
			case "bundle_provenance":
				m.SourceSHA = strings.Repeat("0", 40)
			case "legacy_collision":
				m.Commands.EditorBundleDigest = m.EditorBundleDigest
			}
			raw, _ := json.Marshal(m)
			if problem == "unknown_commands" {
				raw = []byte(strings.Replace(string(raw), `"format":"wordcraft-c1-v2"`, `"unexpected":true,"format":"wordcraft-c1-v2"`, 1))
			}
			source["embedded/generated/manifest.json"].Data = raw
			parent := t.TempDir()
			if _, e := prepare(source, parent); !errors.Is(e, ErrIntegrity) {
				t.Fatal("bad command asset accepted", e)
			}
			entries, _ := os.ReadDir(parent)
			if len(entries) != 0 {
				t.Fatal("failed verification extracted a file")
			}
		})
	}
}
func TestCommandsEditorPackagesContainsBothButNoExecutables(t *testing.T) {
	source := commandFixture(t)
	source["embedded/generated/editor/legacy/index.html"] = &fstest.MapFile{Data: []byte("legacy unchanged")}
	packages, e := editorPackages(source)
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	_ = json.Unmarshal(source["embedded/generated/manifest.json"].Data, &m)
	for _, name := range []string{"legacy/index.html", m.Commands.EditorBundleDigest + "/child.js"} {
		if _, e := fs.ReadFile(packages, name); e != nil {
			t.Fatal(e)
		}
	}
	for _, name := range []string{"../command-host", "command-engine", "../licenses/manifest.json"} {
		if _, e := fs.ReadFile(packages, name); e == nil {
			t.Fatal("adjacent executable/license exposed")
		}
	}
}

func TestCommandsCleanupPreservesUnknownFileInsideOwnedDirectory(t *testing.T) {
	p, e := prepare(commandFixture(t), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	unknown := filepath.Join(p.directory, "not-owned")
	if e = os.WriteFile(unknown, []byte("retain"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = p.Close(); e == nil {
		t.Fatal("nonempty private directory removed")
	}
	if body, e := os.ReadFile(unknown); e != nil || string(body) != "retain" {
		t.Fatal("unknown file removed")
	}
	for _, name := range []string{p.Path, p.CommandHostPath, p.CommandEnginePath} {
		if _, e = os.Stat(name); !errors.Is(e, fs.ErrNotExist) {
			t.Fatal("owned executable retained")
		}
	}
}
