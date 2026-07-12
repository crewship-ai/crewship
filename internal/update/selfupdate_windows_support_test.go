package update

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// #946: Windows release archives are .zip (goreleaser format_overrides) and
// the binary inside is crewship.exe. assetNameFor / binaryNameFor are the
// GOOS-injectable cores AssetNameForTag and PrepareInstallerUpdate use.
func TestAssetNameForWindows(t *testing.T) {
	if got, want := assetNameFor("v0.2.0", false, "windows", "amd64"), "crewship_0.2.0_windows_amd64.zip"; got != want {
		t.Errorf("full = %q, want %q", got, want)
	}
	if got, want := assetNameFor("v0.2.0", true, "windows", "arm64"), "crewship-cli_0.2.0_windows_arm64.zip"; got != want {
		t.Errorf("cli = %q, want %q", got, want)
	}
	// Unix stays .tar.gz — the historical contract.
	if got, want := assetNameFor("v0.2.0", false, "linux", "amd64"), "crewship_0.2.0_linux_amd64.tar.gz"; got != want {
		t.Errorf("linux = %q, want %q", got, want)
	}
	if got, want := binaryNameFor("windows"), "crewship.exe"; got != want {
		t.Errorf("windows binary = %q", got)
	}
	if got, want := binaryNameFor("linux"), "crewship"; got != want {
		t.Errorf("linux binary = %q", got)
	}
}

// extractArchive must sniff the container format: zip archives (Windows)
// and gzip tarballs (unix) both arrive through the same code path.
func TestExtractArchiveZip(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{
		"crewship.exe":     "MZ fake exe",
		"crewship-sidecar": "ELF fake sidecar",
		"entrypoint.sh":    "#!/bin/sh",
		"LICENSE":          "apache",
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	files, err := extractArchive(buf.Bytes())
	if err != nil {
		t.Fatalf("extractArchive(zip): %v", err)
	}
	if string(files["crewship.exe"]) != "MZ fake exe" {
		t.Errorf("crewship.exe missing/wrong: %q", files["crewship.exe"])
	}
	if string(files["crewship-sidecar"]) != "ELF fake sidecar" {
		t.Errorf("crewship-sidecar missing/wrong")
	}
	if _, ok := files["LICENSE"]; ok {
		t.Error("LICENSE should be filtered out (not in wanted set)")
	}
}

func TestExtractArchiveRejectsGarbage(t *testing.T) {
	if _, err := extractArchive([]byte("neither zip nor gzip")); err == nil {
		t.Error("garbage input must error")
	}
}

// replaceViaRenameAside is the Windows swap primitive: the destination is
// first moved aside to dst+".old" (legal even for a running exe on Windows),
// then the payload lands at dst. On unix we can exercise the full sequence
// with plain files.
func TestReplaceViaRenameAside(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "crewship.exe")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(dir, ".crewship-update-1")
	if err := os.WriteFile(tmp, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := replaceViaRenameAside(tmp, dst); err != nil {
		t.Fatalf("replaceViaRenameAside: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "new" {
		t.Errorf("dst = %q, want new payload", got)
	}
	aside, _ := os.ReadFile(dst + swapAsideSuffix)
	if string(aside) != "old" {
		t.Errorf("aside = %q, want old payload preserved", aside)
	}

	// Cleanup helper removes the parked copy.
	CleanupSwapArtifacts(dst)
	if _, err := os.Stat(dst + swapAsideSuffix); !os.IsNotExist(err) {
		t.Error(".old survived CleanupSwapArtifacts")
	}
}
