package update

// Tests for the nightly self-update upgrade path (#1291 point 3): asset
// discovery from the release's published assets (nightly archives carry a
// goreleaser snapshot version that cannot be derived from the tag),
// per-channel cosign identity pinning (nightly.yml signs nightly checksums),
// and the on-disk cache for the nightly update check.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// nightlyReleaseAssets mirrors the real asset listing of a nightly release
// (goreleaser snapshot naming + signature/SBOM/package companions), verified
// against nightly-20260722-r644.
var nightlyReleaseAssets = []string{
	"checksums.txt",
	"checksums.txt.pem",
	"checksums.txt.sig",
	"crewship-cli_0.0.0-snapshot-862e530b_darwin_arm64.tar.gz",
	"crewship-cli_0.0.0-snapshot-862e530b_darwin_arm64.tar.gz.sig",
	"crewship-cli_0.0.0-snapshot-862e530b_linux_amd64.tar.gz",
	"crewship-cli_0.0.0-snapshot-862e530b_linux_amd64.tar.gz.pem",
	"crewship-cli_0.0.0-snapshot-862e530b_linux_amd64.tar.gz.sig",
	"crewship-cli_0.0.0-snapshot-862e530b_linux_amd64.tar.gz.spdx.json",
	"crewship-cli_0.0.0-snapshot-862e530b_windows_amd64.zip",
	"crewship_0.0.0-snapshot-862e530b_darwin_arm64.tar.gz",
	"crewship_0.0.0-snapshot-862e530b_linux_amd64.deb",
	"crewship_0.0.0-snapshot-862e530b_linux_amd64.rpm",
	"crewship_0.0.0-snapshot-862e530b_linux_amd64.tar.gz",
	"crewship_0.0.0-snapshot-862e530b_linux_amd64.tar.gz.cdx.json",
	"crewship_0.0.0-snapshot-862e530b_linux_amd64.tar.gz.sig",
	"crewship_0.0.0-snapshot-862e530b_linux_arm64.tar.gz",
	"crewship_0.0.0-snapshot-862e530b_windows_amd64.zip",
	"crewship_0.0.0-snapshot-862e530b_windows_amd64.zip.sig",
}

// TestSelectAsset pins asset discovery: nightly (snapshot-named) archives are
// found by family prefix + platform suffix, stable archives by the exact
// tag-derived name, and a missing or ambiguous archive is a descriptive error
// — never a filename guess that 404s at download time.
func TestSelectAsset(t *testing.T) {
	stableAssets := []string{
		"checksums.txt",
		"crewship-cli_0.1.0_linux_amd64.tar.gz",
		"crewship_0.1.0_linux_amd64.tar.gz",
		"crewship_0.1.0_linux_amd64.tar.gz.sig",
		"crewship_0.1.0_windows_amd64.zip",
	}
	cases := []struct {
		name    string
		assets  []string
		tag     string
		cliOnly bool
		goos    string
		goarch  string
		want    string
		wantErr string // substring the error must carry; empty = no error
	}{
		{
			name:   "nightly full linux/amd64 picks the snapshot tarball, not deb/rpm/sig",
			assets: nightlyReleaseAssets, tag: "nightly-20260722-r644",
			goos: "linux", goarch: "amd64",
			want: "crewship_0.0.0-snapshot-862e530b_linux_amd64.tar.gz",
		},
		{
			name:   "nightly cli linux/amd64 picks the crewship-cli family, not the full archive",
			assets: nightlyReleaseAssets, tag: "nightly-20260722-r644", cliOnly: true,
			goos: "linux", goarch: "amd64",
			want: "crewship-cli_0.0.0-snapshot-862e530b_linux_amd64.tar.gz",
		},
		{
			name:   "nightly full darwin/arm64",
			assets: nightlyReleaseAssets, tag: "nightly-20260722-r644",
			goos: "darwin", goarch: "arm64",
			want: "crewship_0.0.0-snapshot-862e530b_darwin_arm64.tar.gz",
		},
		{
			name:   "nightly full windows/amd64 picks the zip, not zip.sig",
			assets: nightlyReleaseAssets, tag: "nightly-20260722-r644",
			goos: "windows", goarch: "amd64",
			want: "crewship_0.0.0-snapshot-862e530b_windows_amd64.zip",
		},
		{
			name:   "nightly missing platform is a clear error, not a 404 later",
			assets: nightlyReleaseAssets, tag: "nightly-20260722-r644", cliOnly: true,
			goos: "linux", goarch: "arm64",
			wantErr: "nightly-20260722-r644",
		},
		{
			name:   "stable exact tag-derived match",
			assets: stableAssets, tag: "v0.1.0",
			goos: "linux", goarch: "amd64",
			want: "crewship_0.1.0_linux_amd64.tar.gz",
		},
		{
			name:   "stable cli exact match",
			assets: stableAssets, tag: "v0.1.0", cliOnly: true,
			goos: "linux", goarch: "amd64",
			want: "crewship-cli_0.1.0_linux_amd64.tar.gz",
		},
		{
			name: "ambiguous candidates refuse rather than guess",
			assets: []string{
				"crewship_0.0.0-snapshot-aaaaaaa_linux_amd64.tar.gz",
				"crewship_0.0.0-snapshot-bbbbbbb_linux_amd64.tar.gz",
			},
			tag:  "nightly-20260722-r644",
			goos: "linux", goarch: "amd64",
			wantErr: "cannot pick",
		},
		{
			name:   "empty asset list is a clear error",
			assets: nil, tag: "nightly-20260722-r644",
			goos: "linux", goarch: "amd64",
			wantErr: "nightly-20260722-r644",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectAsset(tc.assets, tc.tag, tc.cliOnly, tc.goos, tc.goarch)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("selectAsset: %v", err)
			}
			if got != tc.want {
				t.Errorf("selectAsset = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestIdentityPatternForTag pins the per-channel cosign identity: nightly
// tags verify against the nightly.yml workflow identity, stable (semver)
// tags against release.yml — and neither channel accepts the other's.
func TestIdentityPatternForTag(t *testing.T) {
	const (
		nightlyURI = "https://github.com/crewship-ai/crewship/.github/workflows/nightly.yml@refs/heads/main"
		releaseURI = "https://github.com/crewship-ai/crewship/.github/workflows/release.yml@refs/tags/v0.1.0"
	)
	nightlyPat := identityPatternForTag("nightly-20260722-r644")
	if !nightlyPat.MatchString(nightlyURI) {
		t.Errorf("nightly tag pattern %v must accept the nightly.yml identity", nightlyPat)
	}
	if nightlyPat.MatchString(releaseURI) {
		t.Errorf("nightly tag pattern %v must reject the release.yml identity", nightlyPat)
	}

	stablePat := identityPatternForTag("v1.2.3")
	if !stablePat.MatchString(releaseURI) {
		t.Errorf("stable tag pattern %v must accept the release.yml identity", stablePat)
	}
	if stablePat.MatchString(nightlyURI) {
		t.Errorf("stable tag pattern %v must reject the nightly.yml identity", stablePat)
	}
}

// newNightlyReleaseFixture wires a fake nightly release end-to-end: a
// snapshot-named archive whose name is only discoverable via the release
// assets API, checksums signed by a test PKI carrying the given workflow
// identity, and the package-level bases pointed at the test server. The
// signature options deliberately leave IdentityPattern nil so the per-tag
// default (identityPatternForTag) is what gets exercised.
func newNightlyReleaseFixture(t *testing.T, identity string) (tag, assetName string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("self-update is gated off on windows")
	}

	tag = "nightly-20260722-r644"
	assetName = fmt.Sprintf("crewship-cli_0.0.0-snapshot-deadbee_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)

	archive := buildTestArchive(t, []byte("#!fake-nightly-binary"))
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), assetName)

	pki := newTestPKI(t, identity, testIssuer)
	assets := map[string][]byte{
		assetName:           archive,
		"checksums.txt":     []byte(checksums),
		"checksums.txt.sig": []byte(pki.sign(t, []byte(checksums))),
		"checksums.txt.pem": pki.leafPEM,
	}

	origOpts := signatureVerifyOpts
	signatureVerifyOpts = SignatureVerifyOptions{
		Roots:      pki.rootPool,
		OIDCIssuer: testIssuer,
		// IdentityPattern left nil on purpose — PrepareInstallerUpdate must
		// pick the channel's pin from the tag.
	}
	t.Cleanup(func() { signatureVerifyOpts = origOpts })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/tags/") {
			names := make([]map[string]string, 0, len(assets))
			for name := range assets {
				names = append(names, map[string]string{"name": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": names})
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		data, ok := assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)

	origDownload := releaseDownloadBase
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = origDownload })
	origAPI := releaseAPIBase
	releaseAPIBase = srv.URL
	t.Cleanup(func() { releaseAPIBase = origAPI })

	return tag, assetName
}

// TestPrepareInstallerUpdate_NightlySnapshotRelease is the end-to-end
// regression for #1291 point 3: a nightly tag whose archive carries a
// snapshot version (underivable from the tag) must be discovered from the
// assets list, and its checksums signature must verify under the nightly.yml
// identity — all with the production defaults (no identity override).
func TestPrepareInstallerUpdate_NightlySnapshotRelease(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	tag, _ := newNightlyReleaseFixture(t,
		"https://github.com/crewship-ai/crewship/.github/workflows/nightly.yml@refs/heads/main")

	prepared, err := PrepareInstallerUpdate(context.Background(), tag, testExePath(t), true, "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("nightly upgrade rejected: %v", err)
	}
	if prepared.toVersion != tag {
		t.Errorf("toVersion = %q, want %q", prepared.toVersion, tag)
	}
}

// TestPrepareInstallerUpdate_NightlyRejectsForeignIdentity proves the nightly
// pin still pins: a checksums file signed under any other workflow identity
// (here release.yml — which never signs nightly tags) must be refused.
func TestPrepareInstallerUpdate_NightlyRejectsForeignIdentity(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	tag, _ := newNightlyReleaseFixture(t,
		"https://github.com/crewship-ai/crewship/.github/workflows/release.yml@refs/tags/v9.9.9")

	_, err := PrepareInstallerUpdate(context.Background(), tag, testExePath(t), true, "nightly-20260721-r638")
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("release.yml-signed nightly must fail the identity pin; got %v", err)
	}
}

// TestCheck_NightlyChannel_UsesCacheWithinTTL pins the fix for the uncached
// nightly check: the first Check hits the network and caches; subsequent
// Checks within the TTL are served from disk (the dashboard polls
// /api/v1/system/version far more often than GitHub's 60 req/h
// unauthenticated quota allows). Newer is still recomputed against the
// caller's current version, so an operator who just upgraded to the cached
// latest sees Newer=false without a refetch.
func TestCheck_NightlyChannel_UsesCacheWithinTTL(t *testing.T) {
	withTempHome(t)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260722-r644", "html_url": "https://x/n644", "body": "n644", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r1, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("first Check: %v", err)
	}
	if r1 == nil || !r1.Newer || r1.Latest != "nightly-20260722-r644" {
		t.Fatalf("first Check = %+v, want newer nightly-20260722-r644", r1)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after first Check = %d, want 1", got)
	}

	r2, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("second Check: %v", err)
	}
	if r2 == nil || !r2.Newer || r2.Latest != "nightly-20260722-r644" {
		t.Fatalf("second Check = %+v, want cached newer result", r2)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests after second Check = %d, want 1 (cache hit)", got)
	}

	// Operator upgrades to the cached latest: still a cache hit, Newer flips.
	r3, err := Check(context.Background(), "nightly-20260722-r644")
	if err != nil {
		t.Fatalf("third Check: %v", err)
	}
	if r3 == nil || r3.Newer {
		t.Errorf("third Check = %+v, want Newer=false against the cached latest", r3)
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("requests after third Check = %d, want 1 (cache hit)", got)
	}
}

// TestCheck_NightlyChannel_ExpiredCacheRefetches proves the nightly TTL is
// honored: a stale cache entry falls through to the network.
func TestCheck_NightlyChannel_ExpiredCacheRefetches(t *testing.T) {
	withTempHome(t)
	writeCache(&Result{
		Current:   "nightly-20260721-r638",
		Latest:    "nightly-20260721-r637",
		Newer:     false,
		CheckedAt: time.Now().UTC().Add(-2 * nightlyCacheTTL),
	})

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260722-r644", "html_url": "https://x", "body": "fresh", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1 (expired cache must refetch)", requests.Load())
	}
	if r == nil || r.Latest != "nightly-20260722-r644" || !r.Newer {
		t.Errorf("Check = %+v, want the fresh nightly", r)
	}
}

// TestCheck_NightlyChannel_IgnoresStableCache: the two channels share one
// cache file; a fresh cached STABLE latest must not satisfy a nightly
// caller (there is no meaningful comparison across channels).
func TestCheck_NightlyChannel_IgnoresStableCache(t *testing.T) {
	withTempHome(t)
	writeCache(&Result{
		Current:   "v0.1.0",
		Latest:    "v0.2.0",
		Newer:     true,
		CheckedAt: time.Now().UTC(),
	})

	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`[{"tag_name": "nightly-20260722-r644", "html_url": "https://x", "body": "n", "draft": false}]`))
	}))
	defer srv.Close()
	restore := setLatestNightlyListURL(srv.URL)
	defer restore()

	r, err := Check(context.Background(), "nightly-20260721-r638")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if requests.Load() != 1 {
		t.Errorf("requests = %d, want 1 (stable cache entry must be ignored)", requests.Load())
	}
	if r == nil || r.Latest != "nightly-20260722-r644" {
		t.Errorf("Check = %+v, want the nightly latest, never the cached stable tag", r)
	}
}

// TestAdaptCachedResult covers the pure cross-channel cache guard, including
// the direction that can't be exercised end-to-end without real network
// (a semver caller finding a cached nightly latest).
func TestAdaptCachedResult(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name      string
		cached    Result
		current   string
		wantNil   bool
		wantNewer bool
	}{
		{
			name:    "nightly current + newer nightly cached",
			cached:  Result{Latest: "nightly-20260722-r644", CheckedAt: now},
			current: "nightly-20260721-r638", wantNewer: true,
		},
		{
			name:    "nightly current + same nightly cached",
			cached:  Result{Latest: "nightly-20260722-r644", Newer: true, CheckedAt: now},
			current: "nightly-20260722-r644", wantNewer: false,
		},
		{
			name:    "nightly current + stable cached is unusable",
			cached:  Result{Latest: "v0.2.0", CheckedAt: now},
			current: "nightly-20260721-r638", wantNil: true,
		},
		{
			name:    "stable current + nightly cached is unusable",
			cached:  Result{Latest: "nightly-20260722-r644", CheckedAt: now},
			current: "v0.1.0", wantNil: true,
		},
		{
			name:    "stable current + newer stable cached",
			cached:  Result{Latest: "v0.2.0", CheckedAt: now},
			current: "v0.1.0", wantNewer: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cached := tc.cached
			got := adaptCachedResult(&cached, tc.current)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("adaptCachedResult = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("adaptCachedResult = nil, want a result")
			}
			if got.Newer != tc.wantNewer {
				t.Errorf("Newer = %v, want %v", got.Newer, tc.wantNewer)
			}
			if got.Current != tc.current {
				t.Errorf("Current = %q, want %q (recomputed for the caller)", got.Current, tc.current)
			}
		})
	}
}

// TestResolveAssetName_StableFallsBackWhenListingFails pins the degradation
// contract: if the assets API is unreachable (rate limit, offline mirror), a
// stable tag still derives its deterministic name — but a nightly tag cannot
// (its snapshot version is unknowable), so the listing error must surface
// instead of a guessed name that would 404.
func TestResolveAssetName_StableFallsBackWhenListingFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // rate-limited
	}))
	defer srv.Close()
	origAPI := releaseAPIBase
	releaseAPIBase = srv.URL
	t.Cleanup(func() { releaseAPIBase = origAPI })

	got, err := resolveAssetName(context.Background(), "v0.1.0", false)
	if err != nil {
		t.Fatalf("stable tag must fall back to the derived name: %v", err)
	}
	if want := AssetNameForTag("v0.1.0", false); got != want {
		t.Errorf("fallback name = %q, want %q", got, want)
	}

	if _, err := resolveAssetName(context.Background(), "nightly-20260722-r644", false); err == nil {
		t.Fatal("nightly tag must surface the listing failure, not guess a name")
	}
}
