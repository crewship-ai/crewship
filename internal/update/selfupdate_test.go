package update

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDetectChannel(t *testing.T) {
	// Pin the go-install inputs so the non-go cases can't accidentally match
	// this machine's real GOBIN / GOPATH / ~/go/bin — including a GOBIN this
	// developer may have persisted with `go env -w` (GOENV=off, the
	// toolchain's own opt-out, keeps that file out of the test).
	t.Setenv("GOENV", "off")
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/nonexistent-gopath")

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

		// npm: the bin shim execs the real binary out of the per-platform
		// package, so the resolved path always carries a node_modules segment.
		// A GLOBAL npm prefix is typically root-owned (writable=false), which is
		// exactly why the npm test must run before the packaged fallback —
		// otherwise these would be misreported as apt/rpm installs.
		{"npm global (root-owned prefix)", "/usr/local/lib/node_modules/@crewship/cli-linux-x64/bin/crewship", false, ChannelNPM},
		{"npm global (user prefix, writable)", "/home/u/.npm-global/lib/node_modules/@crewship/cli-darwin-arm64/bin/crewship", true, ChannelNPM},
		{"npm local project install", "/home/u/proj/node_modules/@crewship/cli-linux-arm64/bin/crewship", true, ChannelNPM},
		{"npx cache", "/home/u/.npm/_npx/a1b2c3/node_modules/@crewship/cli-darwin-arm64/bin/crewship", true, ChannelNPM},
		// Segment matching, not substring: a user directory that merely spells
		// node_modules inside a longer name is NOT an npm install, and must
		// keep its ordinary installer classification.
		{"lookalike dir is not npm", "/home/u/my_node_modules_backup/bin/crewship", true, ChannelInstaller},
		{"lookalike dir is not npm (suffix)", "/home/u/bin/node_modules_old/crewship", true, ChannelInstaller},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DetectChannel(tc.execPath, tc.writable); got != tc.want {
				t.Errorf("DetectChannel(%q, %v) = %v, want %v", tc.execPath, tc.writable, got, tc.want)
			}
		})
	}
}

// TestDetectChannel_GoInstall pins the latent-bug fix: a `go install`-ed
// binary lands in a WRITABLE GOBIN / GOPATH/bin / ~/go/bin, so before this it
// classified as ChannelInstaller and self-update would have overwritten a
// locally-built binary with a downloaded release tarball. Each case drives the
// three sources go itself honours, from the env DetectChannel reads.
func TestDetectChannel_GoInstall(t *testing.T) {
	home := t.TempDir()

	cases := []struct {
		name     string
		gobin    string
		gopath   string
		execPath string
		want     Channel
	}{
		{
			name:     "GOBIN wins",
			gobin:    "/opt/gobin",
			gopath:   "/nonexistent-gopath",
			execPath: "/opt/gobin/crewship",
			want:     ChannelGoInstall,
		},
		{
			// GOBIN set means `go install` writes ONLY there, so GOPATH/bin is
			// not a go-install location in this configuration.
			name:     "GOPATH/bin ignored while GOBIN is set",
			gobin:    "/opt/gobin",
			gopath:   "/home/u/go",
			execPath: "/home/u/go/bin/crewship",
			want:     ChannelInstaller,
		},
		{
			name:     "GOPATH/bin when GOBIN unset",
			gopath:   "/home/u/go",
			execPath: "/home/u/go/bin/crewship",
			want:     ChannelGoInstall,
		},
		{
			// GOPATH is a LIST; every entry's bin is a go-install target.
			name:     "second GOPATH entry",
			gopath:   "/home/u/go" + string(os.PathListSeparator) + "/srv/go",
			execPath: "/srv/go/bin/crewship",
			want:     ChannelGoInstall,
		},
		{
			name:     "default ~/go/bin when GOPATH unset",
			execPath: filepath.Join(home, "go", "bin", "crewship"),
			want:     ChannelGoInstall,
		},
		{
			// Only the bin dir itself counts — a nested dir under GOPATH/bin is
			// not something `go install` produces.
			name:     "nested under GOPATH/bin is not go install",
			gopath:   "/home/u/go",
			execPath: "/home/u/go/bin/vendored/crewship",
			want:     ChannelInstaller,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GOENV", "off") // ignore this developer's `go env -w` file
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home) // windows equivalent of HOME
			t.Setenv("GOBIN", tc.gobin)
			t.Setenv("GOPATH", tc.gopath)
			// writable=true throughout: that is precisely the state that used
			// to mask the bug (a writable dir looked like an installer install).
			if got := DetectChannel(tc.execPath, true); got != tc.want {
				t.Errorf("DetectChannel(%q, true) = %v, want %v", tc.execPath, got, tc.want)
			}
		})
	}
}

// TestDetectChannel_GoEnvFile covers the OTHER way GOBIN/GOPATH get set:
// `go env -w`, which persists them to os.UserConfigDir()/go/env and never
// touches the process environment. It is the officially recommended way to set
// GOBIN persistently, so without reading that file the go-install guard would
// miss exactly the users most likely to have one — and self-update would
// clobber their source-built binary.
//
// Precedence mirrors the toolchain: the OS environment variable wins; the file
// is consulted only when the variable is unset or empty.
func TestDetectChannel_GoEnvFile(t *testing.T) {
	cases := []struct {
		name string
		// envFile is the go env file's content; "" means write no file at all.
		envFile  string
		writeEnv bool
		gobin    string // OS environment, "" = unset
		gopath   string
		execPath string
		want     Channel
	}{
		{
			name:     "GOBIN from the go env file",
			envFile:  "GOBIN=/opt/gobin\nGOTOOLCHAIN=local\n",
			writeEnv: true,
			execPath: "/opt/gobin/crewship",
			want:     ChannelGoInstall,
		},
		{
			name:     "GOPATH from the go env file",
			envFile:  "GOPATH=/srv/go\n",
			writeEnv: true,
			execPath: "/srv/go/bin/crewship",
			want:     ChannelGoInstall,
		},
		{
			// The OS env wins, so the file's GOBIN is NOT a go-install dir here.
			name:     "OS env GOBIN overrides a conflicting file GOBIN",
			envFile:  "GOBIN=/from-file/bin\n",
			writeEnv: true,
			gobin:    "/from-env/bin",
			execPath: "/from-file/bin/crewship",
			want:     ChannelInstaller,
		},
		{
			name:     "OS env GOBIN overrides a conflicting file GOBIN (positive)",
			envFile:  "GOBIN=/from-file/bin\n",
			writeEnv: true,
			gobin:    "/from-env/bin",
			execPath: "/from-env/bin/crewship",
			want:     ChannelGoInstall,
		},
		{
			name:     "OS env GOPATH overrides a conflicting file GOPATH",
			envFile:  "GOPATH=/from-file/go\n",
			writeEnv: true,
			gopath:   "/from-env/go",
			execPath: "/from-file/go/bin/crewship",
			want:     ChannelInstaller,
		},
		{
			// A malformed file must be tolerated silently, never error — it is
			// not ours to validate, and a self-update must not die over it.
			name:     "garbage file is ignored",
			envFile:  "not a key value line\n\n=novalue\nGOBIN\n#comment=x\n",
			writeEnv: true,
			execPath: "/opt/gobin/crewship",
			want:     ChannelInstaller,
		},
		{
			// The common case: no file has ever been written.
			name:     "missing file is not an error",
			writeEnv: false,
			execPath: "/opt/gobin/crewship",
			want:     ChannelInstaller,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// GOENV points the toolchain (and us) at an alternate env file —
			// which is how these cases stay hermetic without a package-level
			// override hook in the production path.
			goenv := filepath.Join(t.TempDir(), "env")
			if tc.writeEnv {
				if err := os.WriteFile(goenv, []byte(tc.envFile), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("GOENV", goenv)
			t.Setenv("GOBIN", tc.gobin)
			t.Setenv("GOPATH", tc.gopath)
			// Keep the ~/go/bin default out of reach of every case.
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)

			if got := DetectChannel(tc.execPath, true); got != tc.want {
				t.Errorf("DetectChannel(%q, true) = %v, want %v", tc.execPath, got, tc.want)
			}
		})
	}
}

// TestGoEnvFileOff pins the toolchain's own opt-out: GOENV=off means "read no
// env file", and we must honour it rather than treating "off" as a path.
func TestGoEnvFileOff(t *testing.T) {
	t.Setenv("GOENV", "off")
	t.Setenv("GOBIN", "")
	t.Setenv("GOPATH", "/nonexistent-gopath")
	if got := DetectChannel("/opt/gobin/crewship", true); got != ChannelInstaller {
		t.Errorf("GOENV=off must disable the env file, got %v", got)
	}
}

// TestChannelString pins the human-readable names (they appear in errors and
// in the --systemd refusal), and in particular that appending the new
// constants did not shift ChannelInstaller off iota 0 / the default arm.
func TestChannelString(t *testing.T) {
	if ChannelInstaller != 0 {
		t.Fatalf("ChannelInstaller must stay iota 0 (it doubles as the default arm), got %d", ChannelInstaller)
	}
	cases := map[Channel]string{
		ChannelInstaller: "installer",
		ChannelHomebrew:  "homebrew",
		ChannelPackaged:  "packaged",
		ChannelNPM:       "npm",
		ChannelGoInstall: "go-install",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Channel(%d).String() = %q, want %q", c, got, want)
		}
	}
}

// TestNPMChannelGuidance pins that the npm refusal is actionable and, for an
// npx run, says the truth: there is nothing to update because npx already
// fetches the latest on every invocation.
func TestNPMChannelGuidance(t *testing.T) {
	global := NPMChannelGuidance("/usr/local/lib/node_modules/@crewship/cli-linux-x64/bin/crewship")
	if !strings.Contains(global, "npm i -g crewship@latest") {
		t.Errorf("global npm guidance must name the upgrade command: %q", global)
	}
	if !strings.Contains(global, "/usr/local/lib/node_modules/@crewship/cli-linux-x64/bin/crewship") {
		t.Errorf("guidance should name the binary path: %q", global)
	}

	npx := NPMChannelGuidance("/home/u/.npm/_npx/a1b2c3/node_modules/@crewship/cli-darwin-arm64/bin/crewship")
	if !strings.Contains(strings.ToLower(npx), "npx") {
		t.Errorf("npx guidance must mention npx: %q", npx)
	}
	if !strings.Contains(strings.ToLower(npx), "nothing to update") {
		t.Errorf("npx guidance must say there is nothing to update: %q", npx)
	}
}

// TestGoInstallChannelGuidance pins the exact module path a user must re-run;
// a wrong path here sends people to a package that doesn't exist.
func TestGoInstallChannelGuidance(t *testing.T) {
	msg := GoInstallChannelGuidance("/home/u/go/bin/crewship")
	if !strings.Contains(msg, "go install github.com/crewship-ai/crewship/cmd/crewship@latest") {
		t.Errorf("guidance must name the real module path: %q", msg)
	}
	if !strings.Contains(msg, "/home/u/go/bin/crewship") {
		t.Errorf("guidance should name the binary path: %q", msg)
	}
}

// TestAssetNameForTag pins the archive name to the goreleaser name_template
// so self-update downloads exactly what CI published — the full build maps to
// crewship_<v>_<os>_<arch>.tar.gz and the CLI-only build to
// crewship-cli_<v>_… (never the other way, or a crewship-cli install would be
// silently swapped for the full server binary).
func TestAssetNameForTag(t *testing.T) {
	full := "crewship_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	cli := "crewship-cli_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if got := AssetNameForTag("v0.1.0", false); got != full {
		t.Errorf("full AssetNameForTag = %q, want %q", got, full)
	}
	if got := AssetNameForTag("0.1.0", false); got != full { // leading v optional
		t.Errorf("full AssetNameForTag (no v) = %q, want %q", got, full)
	}
	if got := AssetNameForTag("v0.1.0", true); got != cli {
		t.Errorf("cli AssetNameForTag = %q, want %q", got, cli)
	}
}

// TestFormulaFromPath pins that the Homebrew formula name is read from the
// Cellar/<formula>/ segment of the resolved path — so a crewship-cli install
// upgrades via `brew upgrade crewship-cli`, not the wrong `crewship` formula.
func TestFormulaFromPath(t *testing.T) {
	cases := []struct {
		path string
		cli  bool
		want string
	}{
		{"/opt/homebrew/Cellar/crewship/0.1.0/bin/crewship", false, "crewship"},
		{"/usr/local/Cellar/crewship-cli/0.1.0/bin/crewship", true, "crewship-cli"},
		{"/home/linuxbrew/.linuxbrew/Cellar/crewship-cli/2.0/bin/crewship", true, "crewship-cli"},
		// No Cellar segment (unexpected): fall back to the build variant.
		{"/opt/homebrew/bin/crewship", true, "crewship-cli"},
		{"/opt/homebrew/bin/crewship", false, "crewship"},
	}
	for _, tc := range cases {
		if got := FormulaFromPath(tc.path, tc.cli); got != tc.want {
			t.Errorf("FormulaFromPath(%q, cli=%v) = %q, want %q", tc.path, tc.cli, got, tc.want)
		}
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

// TestRestoreBackup pins the rollback path: after the binary is backed up
// and swapped, RestoreBackup puts the original bytes back at exePath — the
// recovery the command runs when the new binary fails its sanity check.
func TestRestoreBackup(t *testing.T) {
	dir := t.TempDir()
	exe := dir + "/crewship"
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Simulate ApplyInstallerUpdate's backup + swap.
	if err := copyFile(exe, exe+backupSuffix); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(exe, []byte("NEW-BROKEN")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "NEW-BROKEN" {
		t.Fatalf("pre-restore = %q", got)
	}
	if err := RestoreBackup(exe); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "OLD" {
		t.Errorf("after restore = %q, want OLD", got)
	}
}

// TestRestoreBackups_AllFiles pins the multi-file rollback: after the binary
// AND a companion are backed up and swapped, RestoreBackups returns BOTH to
// their prior bytes — so a failed sanity check never leaves a new binary
// beside a stale (or new-but-orphaned) sidecar.
func TestRestoreBackups_AllFiles(t *testing.T) {
	dir := t.TempDir()
	exe := dir + "/crewship"
	side := dir + "/crewship-sidecar"
	if err := os.WriteFile(exe, []byte("OLD-BIN"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(side, []byte("OLD-SIDE"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{exe, side} {
		if err := copyFile(p, p+backupSuffix); err != nil {
			t.Fatal(err)
		}
	}
	if err := atomicReplace(exe, []byte("NEW-BIN")); err != nil {
		t.Fatal(err)
	}
	if err := atomicReplace(side, []byte("NEW-SIDE")); err != nil {
		t.Fatal(err)
	}
	if err := RestoreBackups([]string{exe, side}); err != nil {
		t.Fatalf("RestoreBackups: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "OLD-BIN" {
		t.Errorf("binary not restored: %q", b)
	}
	if b, _ := os.ReadFile(side); string(b) != "OLD-SIDE" {
		t.Errorf("companion not restored: %q", b)
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
