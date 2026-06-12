package devcontainer

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	ggcrtypes "github.com/google/go-containerregistry/pkg/v1/types"
)

// --- in-memory OCI registry helpers -----------------------------------------

// covStartRegistry spins up an in-memory OCI registry and returns its
// host:port. go-containerregistry treats 127.0.0.1 registries as plain HTTP,
// so no TLS shenanigans are needed.
func covStartRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// covTarBytes builds an in-memory tar archive from a name→content map.
func covTarBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for fname, content := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: fname, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// covPushFeature pushes a single-layer feature artifact to the registry and
// returns the full reference plus the image digest.
func covPushFeature(t *testing.T, host, repoTag string, files map[string]string) (string, string) {
	t.Helper()
	layer := static.NewLayer(covTarBytes(t, files),
		ggcrtypes.MediaType("application/vnd.devcontainers.layer.v1+tar"))
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("building feature image: %v", err)
	}
	ref := host + "/" + repoTag
	return ref, covPushImage(t, ref, img)
}

func covPushImage(t *testing.T, ref string, img v1.Image) string {
	t.Helper()
	parsed, err := name.ParseReference(ref)
	if err != nil {
		t.Fatalf("parsing ref %q: %v", ref, err)
	}
	if err := remote.Write(parsed, img); err != nil {
		t.Fatalf("pushing %q: %v", ref, err)
	}
	dig, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return dig.String()
}

// --- Download / pull ---------------------------------------------------------

func TestDownload_PullsFromRegistryAndCaches(t *testing.T) {
	t.Parallel()

	host := covStartRegistry(t)
	ref, _ := covPushFeature(t, host, "features/testfeat:1", map[string]string{
		"install.sh":                "#!/bin/sh\necho ok",
		"devcontainer-feature.json": `{"id":"testfeat","version":"1","containerEnv":{"FOO":"bar"}}`,
	})

	cacheDir := t.TempDir()
	d := NewFeatureDownloader(cacheDir, testLogger())

	feat, err := d.Download(context.Background(), ref, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if feat.Metadata.ID != "testfeat" {
		t.Errorf("metadata ID = %q, want testfeat", feat.Metadata.ID)
	}
	if feat.Metadata.ContainerEnv["FOO"] != "bar" {
		t.Errorf("containerEnv = %#v", feat.Metadata.ContainerEnv)
	}
	if feat.Ref != ref {
		t.Errorf("Ref = %q, want %q", feat.Ref, ref)
	}
	// Extracted content lands in the cache and is marked cached.
	if !d.IsCached(ref) {
		t.Error("feature should be cached after Download")
	}
	data, err := os.ReadFile(filepath.Join(feat.Dir, "install.sh"))
	if err != nil || !strings.Contains(string(data), "echo ok") {
		t.Errorf("install.sh not extracted correctly: %q, %v", data, err)
	}
	// No leftover temp dirs from the atomic rename.
	entries, _ := os.ReadDir(cacheDir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover extraction temp dir %s", e.Name())
		}
	}
}

func TestPull_ErrorPaths(t *testing.T) {
	t.Parallel()

	host := covStartRegistry(t)

	// An artifact whose layer is missing install.sh.
	noInstallRef, _ := covPushFeature(t, host, "features/noinstall:1", map[string]string{
		"devcontainer-feature.json": `{"id":"noinstall","version":"1"}`,
	})
	// An artifact whose layer is missing the metadata file.
	noMetaRef, _ := covPushFeature(t, host, "features/nometa:1", map[string]string{
		"install.sh": "echo hi",
	})
	// An image with zero layers.
	emptyRef := host + "/features/zerolayers:1"
	covPushImage(t, emptyRef, empty.Image)

	d := NewFeatureDownloader(t.TempDir(), testLogger())
	ctx := context.Background()

	tests := []struct {
		name    string
		ref     string
		wantSub string
	}{
		{"unparseable ref", "not a valid ref!!", "parsing OCI reference"},
		{"unknown repo", host + "/features/missing:1", "fetching image"},
		{"zero layers", emptyRef, "has no layers"},
		{"missing install.sh", noInstallRef, "missing install.sh"},
		{"missing metadata", noMetaRef, "missing devcontainer-feature.json"},
	}
	for _, tt := range tests {
		err := d.pull(ctx, tt.ref, d.cachePathFor(tt.ref))
		if err == nil {
			t.Errorf("%s: expected error", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSub) {
			t.Errorf("%s: error = %v, want substring %q", tt.name, err, tt.wantSub)
		}
		// Failed pulls must not leave a usable cache entry behind.
		if d.IsCached(tt.ref) {
			t.Errorf("%s: failed pull left a cached entry", tt.name)
		}
	}
}

func TestDownload_WrapsPullError(t *testing.T) {
	t.Parallel()

	d := NewFeatureDownloader(t.TempDir(), testLogger())
	_, err := d.Download(context.Background(), "definitely not an oci ref", nil)
	if err == nil || !strings.Contains(err.Error(), "downloading feature") {
		t.Errorf("expected wrapped download error, got %v", err)
	}
}

func TestDownload_CachedButCorruptMetadata(t *testing.T) {
	t.Parallel()

	cacheDir := t.TempDir()
	d := NewFeatureDownloader(cacheDir, testLogger())
	ref := "ghcr.io/x/features/corrupt:1"
	dir := d.cachePathFor(ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "install.sh"), []byte("echo"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-feature.json"), []byte("{invalid"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := d.Download(context.Background(), ref, nil)
	if err == nil || !strings.Contains(err.Error(), "reading cached metadata") {
		t.Errorf("expected cached-metadata error, got %v", err)
	}
}

func TestCreateExtractTempDir_ParentUncreatable(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	blocker := filepath.Join(base, "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := createExtractTempDir(filepath.Join(blocker, "parent", "dest"))
	if err == nil || !strings.Contains(err.Error(), "ensuring temp dir parent") {
		t.Errorf("expected parent-creation error, got %v", err)
	}
}

// --- extractTarGz ------------------------------------------------------------

func TestExtractTarGz_OversizeEntryRejected(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// Header claims 51 MB; the size check fires before any body is read,
	// so no body needs to be written (and the writer is deliberately not
	// closed — Close would complain about the missing body).
	if err := tw.WriteHeader(&tar.Header{
		Name: "huge.bin", Mode: 0o644, Size: 51 << 20, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}

	err := extractTarGz(&buf, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exceeds max size") {
		t.Errorf("expected per-file size cap error, got %v", err)
	}
}

func TestExtractTarGz_TruncatedEntryErrors(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{
		Name: "short.txt", Mode: 0o644, Size: 10, Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil { // 3 of the claimed 10 bytes
		t.Fatal(err)
	}
	// Intentionally no Close: the stream ends mid-entry.

	if err := extractTarGz(&buf, t.TempDir()); err == nil {
		t.Error("expected error for truncated tar entry")
	}
}

func TestExtractTarGz_GarbageStreamErrors(t *testing.T) {
	t.Parallel()

	garbage := bytes.Repeat([]byte{'x'}, 1024)
	err := extractTarGz(bytes.NewReader(garbage), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "reading tar entry") {
		t.Errorf("expected tar-entry read error, got %v", err)
	}
}

func TestExtractTarGz_DefaultsZeroModeAndSkipsLinks(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	writeHdr := func(h *tar.Header, body string) {
		t.Helper()
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeHdr(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	writeHdr(&tar.Header{Name: "zero-mode.txt", Typeflag: tar.TypeReg, Mode: 0, Size: 2}, "hi")
	writeHdr(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}, "")
	writeHdr(&tar.Header{Name: "hardlink", Typeflag: tar.TypeLink, Linkname: "zero-mode.txt"}, "")
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	if err := extractTarGz(&buf, dest); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "zero-mode.txt"))
	if err != nil {
		t.Fatalf("zero-mode.txt missing: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("zero mode should default to 0644, got %v", info.Mode().Perm())
	}
	for _, skipped := range []string{"link", "hardlink"} {
		if _, err := os.Lstat(filepath.Join(dest, skipped)); !os.IsNotExist(err) {
			t.Errorf("%s should have been skipped, lstat err = %v", skipped, err)
		}
	}
}

// --- FeatureMetadata.UnmarshalJSON -------------------------------------------

func TestFeatureMetadata_InstallsAfterForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			"object form with empty id dropped",
			`{"id":"x","installsAfter":[{"id":"a"},{"id":""},{"id":"b"}]}`,
			[]string{"a", "b"},
		},
		{
			"string form",
			`{"id":"x","installsAfter":["c","d"]}`,
			[]string{"c", "d"},
		},
		{"null skipped", `{"id":"x","installsAfter":null}`, nil},
		{"unparseable form skipped", `{"id":"x","installsAfter":42}`, nil},
		{"absent", `{"id":"x"}`, nil},
	}
	for _, tt := range tests {
		var m FeatureMetadata
		if err := json.Unmarshal([]byte(tt.input), &m); err != nil {
			t.Errorf("%s: unmarshal: %v", tt.name, err)
			continue
		}
		if tt.want == nil {
			if len(m.InstallsAfter) != 0 {
				t.Errorf("%s: InstallsAfter = %#v, want empty", tt.name, m.InstallsAfter)
			}
			continue
		}
		if !reflect.DeepEqual(m.InstallsAfter, tt.want) {
			t.Errorf("%s: InstallsAfter = %#v, want %#v", tt.name, m.InstallsAfter, tt.want)
		}
	}
}

func TestFeatureMetadata_UnmarshalInvalidJSON(t *testing.T) {
	t.Parallel()

	var m FeatureMetadata
	if err := json.Unmarshal([]byte(`["not","an","object"]`), &m); err == nil {
		t.Error("expected error unmarshaling non-object into FeatureMetadata")
	}
}

// --- SortFeatures ------------------------------------------------------------

func covFeature(id string, installsAfter ...string) *ResolvedFeature {
	return &ResolvedFeature{
		Ref:      "ghcr.io/x/features/" + id + ":1",
		Metadata: FeatureMetadata{ID: id, InstallsAfter: installsAfter},
	}
}

func TestSortFeatures_SingleAndEmptyPassThrough(t *testing.T) {
	t.Parallel()

	single := []*ResolvedFeature{covFeature("only")}
	if got := SortFeatures(single); len(got) != 1 || got[0].Metadata.ID != "only" {
		t.Errorf("single feature mishandled: %s", featureIDs(got))
	}
	if got := SortFeatures(nil); got != nil {
		t.Errorf("nil input should pass through, got %v", got)
	}
}

func TestSortFeatures_CycleFallsBackToOriginalOrder(t *testing.T) {
	t.Parallel()

	feats := []*ResolvedFeature{
		covFeature("a", "b"),
		covFeature("b", "a"),
		covFeature("c"),
	}
	got := SortFeatures(feats)
	if len(got) != 3 {
		t.Fatalf("cycle dropped features: %s", featureIDs(got))
	}
	// c has no deps so it is emitted first; the a<->b cycle is appended in
	// original order.
	if featureIDs(got) != "c,a,b" {
		t.Errorf("order = %s, want c,a,b", featureIDs(got))
	}
}

func TestSortFeatures_UnknownDependencyIgnored(t *testing.T) {
	t.Parallel()

	feats := []*ResolvedFeature{
		covFeature("a", "not-in-set"),
		covFeature("b"),
	}
	got := SortFeatures(feats)
	if featureIDs(got) != "a,b" {
		t.Errorf("order = %s, want a,b (external dep ignored, stable order)", featureIDs(got))
	}
}
