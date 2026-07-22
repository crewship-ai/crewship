package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// buildTestArchive returns a gzip'd tar containing a fake crewship binary.
func buildTestArchive(t *testing.T, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	if err := tw.WriteHeader(&tar.Header{Name: "crewship", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// sigGateFixture wires a full fake release: archive + checksums + cosign-
// style signature assets served over httptest, with the package-level
// download base and verify options pointed at it for the test's duration.
type sigGateFixture struct {
	assets map[string][]byte
}

func newSigGateFixture(t *testing.T, sign bool) *sigGateFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("self-update is gated off on windows")
	}

	archive := buildTestArchive(t, []byte("#!fake-binary-v2"))
	asset := AssetNameForTag("v9.9.9", true)
	sum := sha256.Sum256(archive)
	checksums := fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), asset)

	f := &sigGateFixture{assets: map[string][]byte{
		asset:           archive,
		"checksums.txt": []byte(checksums),
	}}

	if sign {
		pki := newTestPKI(t, testIdentity, testIssuer)
		f.assets["checksums.txt.sig"] = []byte(pki.sign(t, []byte(checksums)))
		f.assets["checksums.txt.pem"] = pki.leafPEM

		origOpts := signatureVerifyOpts
		signatureVerifyOpts = SignatureVerifyOptions{
			Roots:           pki.rootPool,
			IdentityPattern: regexp.MustCompile(`^https://github\.com/test-org/test-repo/`),
			OIDCIssuer:      testIssuer,
		}
		t.Cleanup(func() { signatureVerifyOpts = origOpts })
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The release-assets API endpoint (asset discovery).
		if strings.HasPrefix(r.URL.Path, "/tags/") {
			names := make([]map[string]string, 0, len(f.assets))
			for name := range f.assets {
				names = append(names, map[string]string{"name": name})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": names})
			return
		}
		name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		data, ok := f.assets[name]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	}))
	t.Cleanup(srv.Close)

	origBase := releaseDownloadBase
	releaseDownloadBase = srv.URL
	t.Cleanup(func() { releaseDownloadBase = origBase })
	origAPI := releaseAPIBase
	releaseAPIBase = srv.URL
	t.Cleanup(func() { releaseAPIBase = origAPI })

	return f
}

func testExePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "crewship")
}

func TestPrepareInstallerUpdate_SignatureHappyPath(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	newSigGateFixture(t, true)

	prepared, err := PrepareInstallerUpdate(context.Background(), "v9.9.9", testExePath(t), true, "1.0.0")
	if err != nil {
		t.Fatalf("signed release rejected: %v", err)
	}
	if prepared.toVersion != "9.9.9" {
		t.Errorf("toVersion = %q, want 9.9.9", prepared.toVersion)
	}
}

func TestPrepareInstallerUpdate_TamperedChecksumsRejected(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	f := newSigGateFixture(t, true)
	// Attacker swaps checksums.txt (and could match it to a malicious
	// archive) — the signature no longer covers the served bytes.
	f.assets["checksums.txt"] = append([]byte("00"), f.assets["checksums.txt"][2:]...)

	_, err := PrepareInstallerUpdate(context.Background(), "v9.9.9", testExePath(t), true, "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("tampered checksums.txt must fail the signature gate; got %v", err)
	}
}

func TestPrepareInstallerUpdate_MissingSignatureRejected(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	f := newSigGateFixture(t, true)
	// Attacker strips the signature assets (downgrade-to-unsigned attack).
	delete(f.assets, "checksums.txt.sig")

	_, err := PrepareInstallerUpdate(context.Background(), "v9.9.9", testExePath(t), true, "1.0.0")
	if err == nil || !strings.Contains(err.Error(), "checksums.txt.sig") {
		t.Fatalf("missing signature must fail closed; got %v", err)
	}
}

func TestPrepareInstallerUpdate_SkipEnvEscapeHatch(t *testing.T) {
	newSigGateFixture(t, false) // unsigned release, no verify-opts override
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "1")

	if _, err := PrepareInstallerUpdate(context.Background(), "v9.9.9", testExePath(t), true, "1.0.0"); err != nil {
		t.Fatalf("escape hatch must allow an unsigned release: %v", err)
	}
}
