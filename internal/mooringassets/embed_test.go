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

func assetFixture(t *testing.T) fstest.MapFS {
	t.Helper()
	binary := []byte("\x7fELFfixture-child")
	hash := sha256.Sum256(binary)
	m := Manifest{SchemaVersion: 1, SourceSHA: strings.Repeat("a", 40), Target: "x86_64-unknown-linux-gnu", Versions: Versions{"0.4.0", "1.0.0", "0.1.0"}, BinarySHA256: hex.EncodeToString(hash[:]), SDK: map[string]string{"index.js": strings.Repeat("b", 64)}, SDKPrefix: "/mooring-sdk/generated/" + strings.Repeat("c", 64), Tools: json.RawMessage(`{}`)}
	sdkRaw, _ := json.Marshal(m.SDK)
	sdkHash := sha256.Sum256(sdkRaw)
	m.SDKPrefix = "/mooring-sdk/generated/" + hex.EncodeToString(sdkHash[:])
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return fstest.MapFS{"embedded/generated/manifest.json": {Data: raw}, "embedded/generated/mooring-collab": {Data: binary}}
}
func TestUnavailableWithoutGeneratedAssets(t *testing.T) {
	_, _, err := read(fstest.MapFS{"embedded/unavailable.json": {Data: []byte(`{"available":false}`)}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
}
func TestPrepareVerifiedPrivateExecutableAndOwnCleanup(t *testing.T) {
	parent := t.TempDir()
	unrelated := filepath.Join(parent, "unrelated")
	if err := os.WriteFile(unrelated, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := prepare(assetFixture(t), parent)
	if err != nil {
		t.Fatal(err)
	}
	original := p.Path
	directory, err := os.Stat(filepath.Dir(original))
	if err != nil || directory.Mode().Perm() != 0o700 {
		t.Fatal(directory, err)
	}
	binary, err := os.Stat(original)
	if err != nil || binary.Mode().Perm() != 0o500 {
		t.Fatal(binary, err)
	}
	// A caller-visible path change must never make cleanup own unrelated files.
	p.Path = unrelated
	if err = p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(original); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("owned file not cleaned", err)
	}
	if data, err := os.ReadFile(unrelated); err != nil || string(data) != "preserve" {
		t.Fatal("unrelated file changed", err)
	}
	if err = p.Close(); err != nil {
		t.Fatal("cleanup not idempotent", err)
	}
}
func TestCorruptAssetsFailBeforeCreatingFile(t *testing.T) {
	for _, kind := range []string{"hash", "elf", "manifest", "target", "prefix", "sdk-path", "sdk-digest"} {
		t.Run(kind, func(t *testing.T) {
			data := assetFixture(t)
			switch kind {
			case "hash":
				data["embedded/generated/mooring-collab"].Data = append(data["embedded/generated/mooring-collab"].Data, 'x')
			case "elf":
				data["embedded/generated/mooring-collab"].Data = []byte("not ELF")
			case "manifest":
				data["embedded/generated/manifest.json"].Data = append(data["embedded/generated/manifest.json"].Data, []byte(` {}`)...)
			default:
				var m Manifest
				if err := json.Unmarshal(data["embedded/generated/manifest.json"].Data, &m); err != nil {
					t.Fatal(err)
				}
				if kind == "target" {
					m.Target = "other"
				}
				if kind == "prefix" {
					m.SDKPrefix = "/other/" + strings.Repeat("c", 64)
				}
				if kind == "sdk-digest" {
					m.SDK["index.js"] = strings.Repeat("d", 64)
				}
				if kind == "sdk-path" {
					m.SDK["../escape.js"] = strings.Repeat("b", 64)
				}
				raw, _ := json.Marshal(m)
				data["embedded/generated/manifest.json"].Data = raw
			}
			parent := t.TempDir()
			if _, err := prepare(data, parent); !errors.Is(err, ErrIntegrity) {
				t.Fatal(err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatal("corrupt asset wrote a file", entries, err)
			}
		})
	}
}
