package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	return assetNameFor(tag, cliOnly, runtime.GOOS, runtime.GOARCH)
}

// assetNameFor is the GOOS/GOARCH-injectable core of AssetNameForTag.
// Windows archives are .zip (goreleaser format_overrides, #945/#946);
// everything else keeps the historical .tar.gz.
func assetNameFor(tag string, cliOnly bool, goos, goarch string) string {
	v := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	prefix := "crewship"
	if cliOnly {
		prefix = "crewship-cli"
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s%s", prefix, v, goos, goarch, ext)
}

// binaryNameFor is the base name of the crewship binary inside a release
// archive for the given GOOS — goreleaser appends .exe on windows.
func binaryNameFor(goos string) string {
	if goos == "windows" {
		return "crewship.exe"
	}
	return "crewship"
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

// releaseDownloadBase is the release asset host; a var so tests can point
// the download path at a local httptest server.
var releaseDownloadBase = "https://github.com/crewship-ai/crewship/releases/download"

// downloadURL is the release asset URL for a given tag + filename.
func downloadURL(tag, name string) string {
	return fmt.Sprintf("%s/%s/%s", releaseDownloadBase, tag, name)
}

// releaseAPIBase is the GitHub API root for this repo's releases — asset
// discovery (resolveAssetName) lists a release's real asset names here. A
// var so tests can point it at a local httptest server.
var releaseAPIBase = "https://api.github.com/repos/crewship-ai/crewship/releases"

// fetchReleaseAssets returns the asset names published on the release `tag`.
func fetchReleaseAssets(ctx context.Context, tag string) ([]string, error) {
	body, err := httpGet(ctx, releaseAPIBase+"/tags/"+tag)
	if err != nil {
		return nil, err
	}
	var rel struct {
		Assets []struct {
			Name string `json:"name"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("parse release JSON: %w", err)
	}
	names := make([]string, 0, len(rel.Assets))
	for _, a := range rel.Assets {
		names = append(names, a.Name)
	}
	return names, nil
}

// selectAsset picks the platform's release archive out of the asset names a
// release actually published.
//
// Stable releases name archives after their tag
// (crewship_<version>_<os>_<arch>.tar.gz), so the tag-derived name matches
// exactly. Nightly releases can't work that way: nightly.yml runs goreleaser
// in snapshot mode (GORELEASER_CURRENT_TAG is a placeholder, see #886), so
// nightly archives carry the 0.0.0-snapshot-<commit> version template —
// unpredictable from the nightly-<date>-r<n> tag, which made a tag-computed
// name a guaranteed 404 (#1291). Discovery therefore prefers the exact
// tag-derived name, then falls back to the unique asset with the right
// family prefix (crewship_ vs crewship-cli_ — never swap variants) and
// _<goos>_<goarch>.<ext> suffix (which signature/SBOM/package companions
// like .sig, .spdx.json, .deb can never match). Zero or multiple candidates
// is a hard, descriptive error — never a guess that 404s at download time.
func selectAsset(assetNames []string, tag string, cliOnly bool, goos, goarch string) (string, error) {
	exact := assetNameFor(tag, cliOnly, goos, goarch)
	prefix := "crewship_"
	if cliOnly {
		prefix = "crewship-cli_"
	}
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	suffix := fmt.Sprintf("_%s_%s%s", goos, goarch, ext)

	var candidates []string
	for _, name := range assetNames {
		if name == exact {
			return name, nil
		}
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 1:
		return candidates[0], nil
	case 0:
		return "", fmt.Errorf(
			"release %s has no %s archive for %s/%s: expected %q or a %s…%s asset (release lists %d assets)",
			tag, strings.TrimSuffix(prefix, "_"), goos, goarch, exact, prefix, suffix, len(assetNames))
	default:
		return "", fmt.Errorf(
			"release %s has %d archives matching %s…%s (%s) — cannot pick one safely",
			tag, len(candidates), prefix, suffix, strings.Join(candidates, ", "))
	}
}

// resolveAssetName discovers the archive asset name for `tag` from the
// release's published assets. If the listing itself fails (GitHub API rate
// limit, an air-gapped mirror serving only the download host), a stable tag
// still derives its deterministic name as a fallback — a nightly tag cannot
// (the snapshot version in its asset names is unknowable without the
// listing), so there the listing error surfaces instead of a guessed name
// that would 404.
func resolveAssetName(ctx context.Context, tag string, cliOnly bool) (string, error) {
	names, err := fetchReleaseAssets(ctx, tag)
	if err != nil {
		if _, nightly := parseNightlyVersion(tag); nightly {
			return "", fmt.Errorf("list release assets for %s: %w", tag, err)
		}
		return AssetNameForTag(tag, cliOnly), nil
	}
	return selectAsset(names, tag, cliOnly, runtime.GOOS, runtime.GOARCH)
}

// signatureVerifyOpts parameterizes the checksums.txt signature gate; the
// zero value selects production pins (embedded Fulcio roots, release.yml
// identity). Tests swap in their own PKI.
var signatureVerifyOpts SignatureVerifyOptions

// skipSignatureVerify is the operator escape hatch for the cosign gate —
// e.g. a fork whose releases sign under a different workflow identity, or
// an air-gapped mirror without signature assets. Ships with the loud
// warning it deserves.
func skipSignatureVerify() bool {
	return os.Getenv("CREWSHIP_SKIP_SIGNATURE_VERIFY") == "1"
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

// maxDownloadBytes bounds a single release download. The full archive is
// ~55 MB; 200 MB leaves generous headroom while capping a hostile or runaway
// response. Exceeding it is an explicit error (below), never a silent
// truncation that would later surface as a confusing checksum mismatch.
const maxDownloadBytes = 200 << 20

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
	// Read one byte past the cap so an oversize body is DETECTED (and errors)
	// rather than silently truncated to maxDownloadBytes.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes (refusing to truncate)", url, maxDownloadBytes)
	}
	return data, nil
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
	prepared, err := PrepareInstallerUpdate(ctx, tag, exePath, cliOnly, fromVersion)
	if err != nil {
		return nil, err
	}
	return prepared.Commit()
}

// PreparedUpdate is a fully downloaded + verified update, staged in memory and
// ready to swap onto disk with Commit. Splitting prepare (network) from commit
// (disk swap) lets the server-upgrade orchestration (RunServerUpdate) fetch and
// verify the new binary BEFORE it stops the service — so a download/checksum
// failure never causes downtime, and the stop→swap→start window is as short as
// a couple of local file writes.
type PreparedUpdate struct {
	targets     []payloadTarget
	fromVersion string
	toVersion   string
	backupPath  string
}

// PrepareInstallerUpdate downloads the release archive for `tag`, verifies its
// sha256 against checksums.txt, extracts it, and assembles the set of files to
// swap (the running binary at exePath plus any companion that both ships in the
// archive and already exists beside it). It performs NO disk changes — a
// failure here leaves the install untouched. binDir (= dir of exePath) must be
// writable at Commit time (installer channel).
func PrepareInstallerUpdate(ctx context.Context, tag, exePath string, cliOnly bool, fromVersion string) (*PreparedUpdate, error) {
	// The asset name is discovered from the release's published assets, not
	// computed from the tag — nightly archives carry a snapshot version the
	// tag doesn't encode (see selectAsset).
	asset, err := resolveAssetName(ctx, tag, cliOnly)
	if err != nil {
		return nil, err
	}
	archive, err := httpGet(ctx, downloadURL(tag, asset))
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset, err)
	}
	checksums, err := httpGet(ctx, downloadURL(tag, "checksums.txt"))
	if err != nil {
		return nil, fmt.Errorf("download checksums.txt: %w", err)
	}
	// Supply-chain gate: checksums.txt ships from the SAME origin as the
	// archive, so its sha256 alone only proves integrity, not authenticity.
	// The cosign keyless signature pins it to this repo's release workflow
	// identity (see sigverify.go) before anything derived from it is trusted.
	if skipSignatureVerify() {
		fmt.Fprintln(os.Stderr, "WARNING: CREWSHIP_SKIP_SIGNATURE_VERIFY=1 — release signature NOT verified; you are trusting the download origin alone")
	} else {
		sig, err := httpGet(ctx, downloadURL(tag, "checksums.txt.sig"))
		if err != nil {
			return nil, fmt.Errorf("download checksums.txt.sig (set CREWSHIP_SKIP_SIGNATURE_VERIFY=1 only if you fully trust the origin): %w", err)
		}
		certPEM, err := httpGet(ctx, downloadURL(tag, "checksums.txt.pem"))
		if err != nil {
			return nil, fmt.Errorf("download checksums.txt.pem: %w", err)
		}
		// Per-channel identity pin: stable tags are signed by release.yml,
		// nightly tags by nightly.yml — pick the matching pin for this tag
		// unless the options (tests, forks) already carry an explicit one.
		opts := signatureVerifyOpts
		if opts.IdentityPattern == nil {
			opts.IdentityPattern = identityPatternForTag(tag)
		}
		if err := VerifyDetachedSignature(checksums, sig, certPEM, opts); err != nil {
			return nil, fmt.Errorf("verify checksums.txt signature: %w", err)
		}
	}
	if err := VerifyChecksum(archive, asset, string(checksums)); err != nil {
		return nil, err
	}

	extracted, err := extractArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", asset, err)
	}
	newBinary, ok := extracted[binaryNameFor(runtime.GOOS)]
	if !ok {
		return nil, fmt.Errorf("archive %s did not contain a %s binary", asset, binaryNameFor(runtime.GOOS))
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

	return &PreparedUpdate{
		targets:     targets,
		fromVersion: fromVersion,
		toVersion:   strings.TrimPrefix(tag, "v"),
		backupPath:  exePath + backupSuffix,
	}, nil
}

// Commit backs up every target then atomically swaps in the prepared bytes. On
// any mid-swap failure it restores every file swapped so far, so the install
// stays consistent (no new-server + stale-sidecar mix). This is the only
// disk-mutating half of an installer update.
func (p *PreparedUpdate) Commit() (*SelfUpdateResult, error) {
	// Back up every target BEFORE swapping any, so a mid-swap failure can
	// restore ALL of them (not just the binary) to a consistent prior state.
	for _, t := range p.targets {
		if err := copyFile(t.path, t.path+backupSuffix); err != nil {
			return nil, fmt.Errorf("back up %s: %w", t.path, err)
		}
	}

	res := &SelfUpdateResult{
		FromVersion: p.fromVersion,
		ToVersion:   p.toVersion,
		BackupPath:  p.backupPath,
	}
	for i, t := range p.targets {
		if err := atomicReplace(t.path, t.data); err != nil {
			// Roll back every file swapped so far (including this failed one's
			// prior state via its backup) so the install stays consistent.
			_ = RestoreBackups(pathsOf(p.targets[:i+1]))
			return nil, fmt.Errorf("replace %s (rolled back %d file(s)): %w", t.path, i+1, err)
		}
		res.Replaced = append(res.Replaced, t.path)
	}
	// A rename-aside swap (windows) parks the previous binary at
	// <path>.old; try to clear the parked copies now. The running exe's
	// own .old survives until the next update run — expected.
	for _, t := range p.targets {
		CleanupSwapArtifacts(t.path)
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
	if err := os.Rename(tmpName, dst); err != nil {
		// Windows refuses to rename over the running executable
		// (ERROR_ACCESS_DENIED); park the destination aside first —
		// renaming a running exe IS legal there — then land the payload
		// (#946). Non-windows rename failures are real errors.
		if runtime.GOOS == "windows" {
			return replaceViaRenameAside(tmpName, dst)
		}
		return err
	}
	return nil
}

// swapAsideSuffix marks the previous binary parked by a rename-aside swap.
// It cannot be deleted while it is the running process's image, so it is
// cleaned up best-effort after the swap and on the next self-update run.
const swapAsideSuffix = ".old"

// replaceViaRenameAside implements the Windows-legal binary swap: move dst
// out of the way to dst+".old" (allowed even while dst is executing), then
// rename the payload into place. The parked .old is left for
// CleanupSwapArtifacts — deleting it here would fail while the old image
// is still mapped.
func replaceViaRenameAside(tmpName, dst string) error {
	aside := dst + swapAsideSuffix
	// A stale .old from an earlier update blocks the park rename once the
	// old process has exited; clear it if possible.
	_ = os.Remove(aside)
	if err := os.Rename(dst, aside); err != nil {
		return fmt.Errorf("park %s aside: %w", dst, err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		// Try to put the original back so the install isn't left headless.
		_ = os.Rename(aside, dst)
		return fmt.Errorf("land new binary at %s: %w", dst, err)
	}
	return nil
}

// CleanupSwapArtifacts best-effort removes the ".old" file a rename-aside
// swap parks next to the binary. Callers invoke it at the start of a
// self-update run (the previous update's .old is no longer executing) and
// after a successful Commit; failures are expected while the parked image
// is still running and are silently ignored.
func CleanupSwapArtifacts(exePath string) {
	_ = os.Remove(exePath + swapAsideSuffix)
}

// extractArchive pulls the crewship binary + companions out of a release
// archive into memory, keyed by base name. The container format is sniffed
// from magic bytes: gzip'd tar for unix archives, zip for windows (#946).
// Other archive members (LICENSE, README) are ignored.
func extractArchive(raw []byte) (map[string][]byte, error) {
	wanted := map[string]bool{"crewship": true, "crewship.exe": true}
	for _, c := range companions {
		wanted[c] = true
	}
	if len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K' {
		return extractZip(raw, wanted)
	}
	return extractTarGz(raw, wanted)
}

// extractZip is the windows-archive branch of extractArchive.
func extractZip(raw []byte, wanted map[string]bool) (map[string][]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		if !wanted[base] {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		// Cap a single member at 200 MB (same bound as the download).
		buf, err := io.ReadAll(io.LimitReader(rc, 200<<20))
		rc.Close()
		if err != nil {
			return nil, err
		}
		out[base] = buf
	}
	return out, nil
}

// extractTarGz is the unix-archive branch of extractArchive.
func extractTarGz(gz []byte, wanted map[string]bool) (map[string][]byte, error) {
	gr, err := gzip.NewReader(strings.NewReader(string(gz)))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
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
