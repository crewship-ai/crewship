package update

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"runtime"
	"strings"
	"testing"
)

func TestDetectChannel(t *testing.T) {
	cases := []struct {
		name     string
		execPath string
		writable bool
		want     Channel
	}{
		{"homebrew cellar (apple silicon)", "/opt/homebrew/Cellar/crewship/0.1.0/bin/crewship", false, ChannelHomebrew},
		{"homebrew cellar (intel)", "/usr/local/Cellar/crewship/0.1.0/bin/crewship", false, ChannelHomebrew},
		{"linuxbrew cellar", "/home/linuxbrew/.linuxbrew/Cellar/crewship/0.1.0/bin/crewship", true, ChannelHomebrew},
		{"installer writable (~/.local/bin)", "/home/u/.local/bin/crewship", true, ChannelInstaller},
		{"installer writable (/usr/local/bin non-brew)", "/usr/local/bin/crewship", true, ChannelInstaller},
		// Not writable and not brew = a system package manager owns it (apt/rpm)
		// or it's a read-only mount — we must not clobber it.
		{"packaged non-writable /usr/bin", "/usr/bin/crewship", false, ChannelPackaged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectChannel(tc.execPath, tc.writable); got != tc.want {
				t.Errorf("DetectChannel(%q, %v) = %v, want %v", tc.execPath, tc.writable, got, tc.want)
			}
		})
	}
}

// TestAssetNameForTag pins the archive name to the goreleaser name_template
// (crewship_<version-without-v>_<os>_<arch>.tar.gz) so self-update downloads
// exactly what CI published — matching scripts/install.sh.
func TestAssetNameForTag(t *testing.T) {
	got := AssetNameForTag("v0.1.0")
	want := "crewship_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if got != want {
		t.Errorf("AssetNameForTag = %q, want %q", got, want)
	}
	// Leading v is optional on input.
	if AssetNameForTag("0.1.0") != want {
		t.Errorf("AssetNameForTag without leading v = %q, want %q", AssetNameForTag("0.1.0"), want)
	}
}

// TestVerifyChecksum pins the checksums.txt match: the sha256 for the exact
// archive filename must equal the computed digest, and a mismatch or a
// missing entry is an error (never a silent pass — this is the supply-chain
// gate).
func TestVerifyChecksum(t *testing.T) {
	// "hello\n" → known sha256.
	data := []byte("hello\n")
	const sum = "5891b5b522d5df086d0ff0b110fbd9d21bb4fc7163af34d08286a2e846f6be03"
	checksums := "deadbeef  other_file.tar.gz\n" + sum + "  crewship_0.1.0_x.tar.gz\n"

	if err := VerifyChecksum(data, "crewship_0.1.0_x.tar.gz", checksums); err != nil {
		t.Errorf("matching checksum should pass: %v", err)
	}
	if err := VerifyChecksum([]byte("tampered"), "crewship_0.1.0_x.tar.gz", checksums); err == nil {
		t.Error("tampered payload must fail checksum verification")
	}
	if err := VerifyChecksum(data, "not_listed.tar.gz", checksums); err == nil {
		t.Error("archive missing from checksums.txt must error")
	}
}

// TestPackagedRefusalNamesCommand pins that the packaged-channel guidance is
// actionable: it names the platform's own upgrade path rather than trying to
// overwrite a file the package manager owns.
func TestPackagedRefusalNamesCommand(t *testing.T) {
	msg := PackagedChannelGuidance("/usr/bin/crewship")
	if !strings.Contains(msg, "/usr/bin/crewship") {
		t.Errorf("guidance should name the binary path: %q", msg)
	}
	for _, want := range []string{"package manager", "install.sh"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("guidance missing %q: %q", want, msg)
		}
	}
}

// TestExtractAndAtomicReplace proves the swap mechanics end-to-end without
// the network: build a tar.gz containing crewship + a companion + noise,
// extract, and atomically replace an existing on-disk file.
func TestExtractAndAtomicReplace(t *testing.T) {
	gz := buildTarGz(t, map[string]string{
		"crewship":         "NEW-BINARY",
		"crewship-sidecar": "NEW-SIDECAR",
		"README.md":        "ignored",
	})
	files, err := extractArchive(gz)
	if err != nil {
		t.Fatalf("extractArchive: %v", err)
	}
	if string(files["crewship"]) != "NEW-BINARY" || string(files["crewship-sidecar"]) != "NEW-SIDECAR" {
		t.Fatalf("extract mismatch: %v", files)
	}
	if _, ok := files["README.md"]; ok {
		t.Error("non-target archive member should be ignored")
	}

	dir := t.TempDir()
	dst := dir + "/crewship"
	if err := os.WriteFile(dst, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(dst, files["crewship"]); err != nil {
		t.Fatalf("atomicReplace: %v", err)
	}
	got, _ := os.ReadFile(dst)
	if string(got) != "NEW-BINARY" {
		t.Errorf("after replace = %q, want NEW-BINARY", got)
	}
	// No temp leftovers in the dir.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("expected only the swapped file, got %d entries", len(entries))
	}
}

func buildTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf strings.Builder
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	return []byte(buf.String())
}
