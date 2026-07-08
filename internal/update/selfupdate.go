package update

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Channel is how the running crewship binary was installed, which decides
// how (or whether) self-update may replace it.
type Channel int

const (
	// ChannelInstaller: a tarball / install.sh install into a directory the
	// user owns. self-update downloads the matching release asset, verifies
	// its checksum, and atomically swaps the binary in place.
	ChannelInstaller Channel = iota
	// ChannelHomebrew: managed by Homebrew (path under a Cellar). self-update
	// delegates to `brew upgrade crewship` rather than fighting the formula.
	ChannelHomebrew
	// ChannelPackaged: the binary sits somewhere this process can't write and
	// isn't Homebrew — a system package manager (apt/rpm), a container image,
	// or a read-only mount owns it. self-update refuses and points elsewhere.
	ChannelPackaged
)

func (c Channel) String() string {
	switch c {
	case ChannelHomebrew:
		return "homebrew"
	case ChannelPackaged:
		return "packaged"
	default:
		return "installer"
	}
}

// DetectChannel classifies an install from the (symlink-resolved) executable
// path and whether its directory is writable by this process. Homebrew wins
// regardless of writability (a brew bin can be user-writable but must still
// be upgraded via brew); otherwise a non-writable location means something
// else owns the file.
func DetectChannel(execPath string, writable bool) Channel {
	if isHomebrewPath(execPath) {
		return ChannelHomebrew
	}
	if !writable {
		return ChannelPackaged
	}
	return ChannelInstaller
}

// isHomebrewPath reports whether the path lives inside a Homebrew Cellar —
// the one marker common to Intel (/usr/local/Cellar), Apple Silicon
// (/opt/homebrew/Cellar), and Linuxbrew (/home/linuxbrew/.linuxbrew/Cellar).
// Callers should resolve symlinks first: `brew` symlinks prefix/bin/crewship
// to the Cellar, and only the resolved target carries the marker.
func isHomebrewPath(p string) bool {
	return strings.Contains(filepath.ToSlash(p), "/Cellar/")
}

// AssetNameForTag returns the release archive filename for this platform,
// mirroring the goreleaser name_template and scripts/install.sh. cliOnly
// selects the crewship-cli_… archive (the clionly build) instead of the full
// crewship_… one — downloading the wrong family would swap a lightweight CLI
// install for the ~2× larger full server binary (or vice-versa).
func AssetNameForTag(tag string, cliOnly bool) string {
	v := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	prefix := "crewship"
	if cliOnly {
		prefix = "crewship-cli"
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", prefix, v, runtime.GOOS, runtime.GOARCH)
}

// FormulaFromPath returns the Homebrew formula name for a brew-managed
// binary, read from the Cellar/<formula>/ segment of the resolved path so a
// crewship-cli install upgrades the crewship-cli formula, not crewship. Falls
// back to the build variant when the path carries no Cellar segment.
func FormulaFromPath(execPath string, cliOnly bool) string {
	parts := strings.Split(filepath.ToSlash(execPath), "/")
	for i, p := range parts {
		if p == "Cellar" && i+1 < len(parts) && parts[i+1] != "" {
			return parts[i+1]
		}
	}
	if cliOnly {
		return "crewship-cli"
	}
	return "crewship"
}

// downloadURL is the release asset URL for a given tag + filename.
func downloadURL(tag, name string) string {
	return fmt.Sprintf("https://github.com/crewship-ai/crewship/releases/download/%s/%s", tag, name)
}

// VerifyChecksum confirms sha256(data) matches the entry for `name` in a
// checksums.txt body ("<sha256>  <filename>" lines). A missing entry or a
// mismatch is an error — this is the supply-chain gate, so it never passes
// on ambiguity.
func VerifyChecksum(data []byte, name, checksums string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	var want string
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			want = fields[0]
			break
		}
	}
	if want == "" {
		return fmt.Errorf("no checksum for %q in checksums.txt", name)
	}
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return nil
}

// PackagedChannelGuidance is the actionable refusal shown when self-update
// can't touch a package-managed / read-only binary.
func PackagedChannelGuidance(execPath string) string {
	return fmt.Sprintf(
		"%s is managed by your system package manager (or lives on a read-only path), "+
			"so 'crewship self-update' won't overwrite it. Upgrade it the way you installed it:\n"+
			"  • apt/deb:   apt-get update && apt-get install --only-upgrade crewship\n"+
			"  • rpm/dnf:   dnf upgrade crewship\n"+
			"  • docker:    docker pull ghcr.io/crewship-ai/crewship:latest\n"+
			"Or reinstall into a directory you own with the official installer:\n"+
			"  curl -fsSL https://raw.githubusercontent.com/crewship-ai/crewship/main/scripts/install.sh | bash",
		execPath,
	)
}

// companions are the runtime files that ship next to the crewship binary in
// the full archive (packaging fix #858). self-update replaces whichever of
// them already sit alongside the binary, so an upgrade doesn't leave a stale
// sidecar behind a new server.
var companions = []string{"crewship-sidecar", "entrypoint.sh"}

// SelfUpdateResult reports what an installer-channel update did.
type SelfUpdateResult struct {
	FromVersion string
	ToVersion   string
	Replaced    []string // paths swapped (the binary + any companions)
	BackupPath  string   // the "<binary>.bak" kept for rollback
}

// backupSuffix is appended to the running binary before it is swapped, so a
// failed sanity check (or the operator) can restore the previous version.
const backupSuffix = ".bak"

// httpGet fetches a URL body with a bounded timeout, failing on non-200.
func httpGet(ctx context.Context, url string) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "crewship-self-update")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	// Cap at 200 MB — the full archive is ~55 MB; this bounds a hostile or
	// runaway response without truncating a legitimate download.
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// ApplyInstallerUpdate downloads the release archive for `tag`, verifies its
// sha256 against checksums.txt, backs up the CURRENT binary to
// "<exePath>.bak", and atomically swaps in the new one — the running binary
// at exePath itself (not a reconstructed <dir>/crewship, so a renamed binary
// is still updated in place), plus any companions already installed beside
// it (full build only; the CLI archive has none). It does NOT decide whether
// an update is warranted — the caller gates on the version check first.
// binDir (= dir of exePath) must be writable (installer channel).
//
// On any error after the backup is taken, the binary is restored so a failed
// update never leaves the operator with a broken install.
func ApplyInstallerUpdate(ctx context.Context, tag, exePath string, cliOnly bool, fromVersion string) (*SelfUpdateResult, error) {
	asset := AssetNameForTag(tag, cliOnly)
	archive, err := httpGet(ctx, downloadURL(tag, asset))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	checksums, err := httpGet(ctx, downloadURL(tag, "checksums.txt"))
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}
	if err := VerifyChecksum(archive, asset, string(checksums)); err != nil {
		return nil, err
	}

	extracted, err := extractArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", asset, err)
	}
	newBinary, ok := extracted["crewship"]
	if !ok {
		return nil, fmt.Errorf("archive %s did not contain a crewship binary", asset)
	}

	binDir := filepath.Dir(exePath)

	// Assemble the full set of files to swap up front: the running binary,
	// then any companion (full build only) that BOTH ships in the archive and
	// already exists beside the binary (don't newly introduce files the
	// install lacked). Every target is swapped and rolled back as one unit —
	// a partial swap (new server + stale sidecar) is worse than no update.
	targets := []payloadTarget{{path: exePath, data: newBinary}}
	if !cliOnly {
		for _, name := range companions {
			payload, inArchive := extracted[name]
			if !inArchive {
				continue
			}
			dst := filepath.Join(binDir, name)
			if _, err := os.Stat(dst); err != nil {
				continue // companion not present in this install — leave as-is
			}
			targets = append(targets, payloadTarget{path: dst, data: payload})
		}
	}

	// Back up every target BEFORE swapping any, so a mid-swap failure can
	// restore ALL of them (not just the binary) to a consistent prior state.
	for _, t := range targets {
		if err := copyFile(t.path, t.path+backupSuffix); err != nil {
			return nil, fmt.Errorf("back up %s: %w", t.path, err)
		}
	}

	res := &SelfUpdateResult{
		FromVersion: fromVersion,
		ToVersion:   strings.TrimPrefix(tag, "v"),
		BackupPath:  exePath + backupSuffix,
	}
	for i, t := range targets {
		if err := atomicReplace(t.path, t.data); err != nil {
			// Roll back every file swapped so far (including this failed one's
			// prior state via its backup) so the install stays consistent.
			_ = RestoreBackups(pathsOf(targets[:i+1]))
			return nil, fmt.Errorf("replace %s (rolled back %d file(s)): %w", t.path, i+1, err)
		}
		res.Replaced = append(res.Replaced, t.path)
	}
	return res, nil
}

// payloadTarget pairs a destination path with the bytes to write there.
type payloadTarget struct {
	path string
	data []byte
}

func pathsOf(ts []payloadTarget) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.path
	}
	return out
}

// RestoreBackup swaps the "<path>.bak" backup back over path — the rollback
// primitive for a single file.
func RestoreBackup(path string) error {
	data, err := os.ReadFile(path + backupSuffix)
	if err != nil {
		return fmt.Errorf("read backup %s: %w", path+backupSuffix, err)
	}
	return atomicReplace(path, data)
}

// RestoreBackups restores every path from its "<path>.bak" backup — the
// rollback the caller invokes when the freshly-swapped binary fails its
// sanity check, so the binary AND its companions return together. It attempts
// every path even if one fails, returning the first error.
func RestoreBackups(paths []string) error {
	var firstErr error
	for _, p := range paths {
		if err := RestoreBackup(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// copyFile copies src → dst preserving 0755, used to stage the pre-update
// backup. dst is overwritten if it exists (a stale .bak from a prior update).
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

// atomicReplace writes payload to a temp file in the same directory, chmod
// 0755, then renames it over dst — atomic on a single filesystem, so a
// crash mid-write never leaves a half-written binary at dst.
func atomicReplace(dst string, payload []byte) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".crewship-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		return err
	}
	return os.Rename(tmpName, dst)
}

// extractArchive pulls the crewship binary + companions out of a gzip'd tar
// into memory, keyed by base name. Other archive members (LICENSE, README)
// are ignored.
func extractArchive(gz []byte) (map[string][]byte, error) {
	gr, err := gzip.NewReader(strings.NewReader(string(gz)))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	wanted := map[string]bool{"crewship": true}
	for _, c := range companions {
		wanted[c] = true
	}
	out := map[string][]byte{}
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		base := filepath.Base(hdr.Name)
		if !wanted[base] {
			continue
		}
		// Cap a single member at 200 MB (same bound as the download).
		buf, err := io.ReadAll(io.LimitReader(tr, 200<<20))
		if err != nil {
			return nil, err
		}
		out[base] = buf
	}
	return out, nil
}
