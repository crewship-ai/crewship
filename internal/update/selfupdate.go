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
// mirroring the goreleaser name_template and scripts/install.sh:
// crewship_<version-without-v>_<os>_<arch>.tar.gz.
func AssetNameForTag(tag string) string {
	v := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	return fmt.Sprintf("crewship_%s_%s_%s.tar.gz", v, runtime.GOOS, runtime.GOARCH)
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
	Replaced    []string // basenames swapped (crewship + any companions)
}

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
// checksum against the signed checksums.txt, and atomically swaps the
// crewship binary (plus any companions already installed beside it) in
// binDir. It does NOT decide whether an update is warranted — the caller
// gates on update.Check first. binDir must be writable (installer channel).
func ApplyInstallerUpdate(ctx context.Context, tag, binDir, fromVersion string) (*SelfUpdateResult, error) {
	asset := AssetNameForTag(tag)
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

	// Extract the files we care about from the tar.gz into memory.
	extracted, err := extractArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w", asset, err)
	}
	if _, ok := extracted["crewship"]; !ok {
		return nil, fmt.Errorf("archive %s did not contain a crewship binary", asset)
	}

	res := &SelfUpdateResult{FromVersion: fromVersion, ToVersion: strings.TrimPrefix(tag, "v")}
	// Always swap crewship; swap a companion only if one already lives beside
	// the binary (don't newly introduce files an existing install lacked).
	for _, name := range append([]string{"crewship"}, companions...) {
		payload, inArchive := extracted[name]
		if !inArchive {
			continue
		}
		dst := filepath.Join(binDir, name)
		if name != "crewship" {
			if _, err := os.Stat(dst); err != nil {
				continue // companion not present in this install — leave as-is
			}
		}
		if err := atomicReplace(dst, payload); err != nil {
			return nil, fmt.Errorf("replace %s: %w", name, err)
		}
		res.Replaced = append(res.Replaced, name)
	}
	return res, nil
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
