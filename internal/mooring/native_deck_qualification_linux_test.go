//go:build linux && craft_native_engine_qualification

package mooring

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

// Qualification-only inputs, never server configuration:
// CRAFT_QUALIFICATION_ENGINE and CRAFT_QUALIFICATION_SHA256 identify the original
// pinned executable; CRAFT_QUALIFICATION_DECK_ARCHIVE is its native ZIP fixture.
// Explicit qualification fails without all inputs; ordinary unit tests exclude it.
func TestNativeCLIValidatorOriginalDeckArchive(t *testing.T) {
	binary, hash, path := os.Getenv("CRAFT_QUALIFICATION_ENGINE"), os.Getenv("CRAFT_QUALIFICATION_SHA256"), os.Getenv("CRAFT_QUALIFICATION_DECK_ARCHIVE")
	if binary == "" || len(hash) != 64 || path == "" {
		t.Fatal("explicit original CLI, SHA256 and native Deck archive required")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.LimitReader(f, deckRawLimit+1))
	closeErr := f.Close()
	if err != nil || closeErr != nil || len(raw) > deckRawLimit {
		t.Fatal("cannot read bounded native fixture")
	}
	validator := NativeCLIValidator{Application: "deckcraft", BinaryPath: binary, SHA256: hash}
	if err := validator.Validate(t.Context(), raw); err != nil {
		t.Fatalf("original pinned Deck archive rejected: %v", err)
	}
	// The original loader discards unknown authored fields. Validation must reject
	// that roundtrip instead of treating a successful load/save as lossless.
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var mutated bytes.Buffer
	writer := zip.NewWriter(&mutated)
	for _, file := range zr.File {
		h := file.FileHeader
		out, err := writer.CreateHeader(&h)
		if err != nil {
			t.Fatal(err)
		}
		in, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		if file.Name == "presentation.json" {
			var manifest map[string]json.RawMessage
			if err = json.NewDecoder(in).Decode(&manifest); err != nil {
				t.Fatal(err)
			}
			manifest["unrecognized_authored_extension"] = json.RawMessage(`"must survive"`)
			encoded, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = out.Write(encoded); err != nil {
				t.Fatal(err)
			}
		} else if _, err = io.Copy(out, in); err != nil {
			t.Fatal(err)
		}
		if err = in.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err = validator.Validate(t.Context(), mutated.Bytes()); err == nil {
		t.Fatal("original engine's authored field loss was accepted")
	}
}
