package devcontainer

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestToFeatureRef(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected FeatureRef
		wantErr  bool
	}{
		{
			name:  "full ref with tag",
			input: "ghcr.io/devcontainers/features/go:1",
			expected: FeatureRef{
				Registry: "ghcr.io",
				Repo:     "devcontainers/features/go",
				Tag:      "1",
			},
		},
		{
			name:  "full ref with latest tag",
			input: "ghcr.io/devcontainers/features/python:latest",
			expected: FeatureRef{
				Registry: "ghcr.io",
				Repo:     "devcontainers/features/python",
				Tag:      "latest",
			},
		},
		{
			name:  "semver tag",
			input: "ghcr.io/devcontainers/features/rust:1.2.3",
			expected: FeatureRef{
				Registry: "ghcr.io",
				Repo:     "devcontainers/features/rust",
				Tag:      "1.2.3",
			},
		},
		{
			name:  "custom registry",
			input: "myregistry.com/myorg/feature:2",
			expected: FeatureRef{
				Registry: "myregistry.com",
				Repo:     "myorg/feature",
				Tag:      "2",
			},
		},
		{
			name:    "no tag",
			input:   "ghcr.io/devcontainers/features/go",
			wantErr: true,
		},
		{
			name:    "no slash",
			input:   "python:1",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToFeatureRef(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.expected {
				t.Errorf("ToFeatureRef(%q) =\n  %+v\nwant\n  %+v", tc.input, got, tc.expected)
			}
		})
	}
}

func TestCacheKey(t *testing.T) {
	key1 := cacheKey("ghcr.io/devcontainers/features/go:1")
	key2 := cacheKey("ghcr.io/devcontainers/features/python:1")
	key3 := cacheKey("ghcr.io/devcontainers/features/go:1")

	if key1 == key2 {
		t.Errorf("different refs should produce different cache keys")
	}
	if key1 != key3 {
		t.Errorf("same ref should produce same cache key, got %q and %q", key1, key3)
	}
	if len(key1) != 16 {
		t.Errorf("cache key should be 16 hex chars, got %d: %q", len(key1), key1)
	}
}

func TestIsCached(t *testing.T) {
	tmpDir := t.TempDir()
	d := NewFeatureDownloader(tmpDir, slog.Default())

	ref := "ghcr.io/devcontainers/features/go:1"

	// Not cached initially.
	if d.IsCached(ref) {
		t.Fatal("expected not cached")
	}

	// Create a fake cache entry — IsCached now requires BOTH install.sh AND
	// devcontainer-feature.json (a partial extraction missing metadata would
	// fail later in readMetadata, so it's treated as cache miss).
	cacheDir := d.cachePathFor(ref)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "install.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Only install.sh → still cache miss (metadata required).
	if d.IsCached(ref) {
		t.Fatal("expected cache miss when devcontainer-feature.json missing")
	}

	// Add the metadata file.
	if err := os.WriteFile(filepath.Join(cacheDir, "devcontainer-feature.json"), []byte(`{"id":"go","version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Now it should be cached.
	if !d.IsCached(ref) {
		t.Fatal("expected cached after creating install.sh + devcontainer-feature.json")
	}
}

func TestDownloadUsesCacheWhenPresent(t *testing.T) {
	tmpDir := t.TempDir()
	d := NewFeatureDownloader(tmpDir, slog.Default())

	ref := "ghcr.io/devcontainers/features/go:1"

	// Prepare a fake cached feature.
	cacheDir := d.cachePathFor(ref)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := FeatureMetadata{
		ID:      "go",
		Version: "1.0.0",
		Name:    "Go",
	}
	metaBytes, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(cacheDir, "devcontainer-feature.json"), metaBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "install.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Download should return cached result without hitting the network.
	resolved, err := d.Download(t.Context(), ref, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Ref != ref {
		t.Errorf("expected ref %q, got %q", ref, resolved.Ref)
	}
	if resolved.Metadata.ID != "go" {
		t.Errorf("expected metadata ID %q, got %q", "go", resolved.Metadata.ID)
	}
	if resolved.Dir != cacheDir {
		t.Errorf("expected dir %q, got %q", cacheDir, resolved.Dir)
	}
}

func TestClearCache(t *testing.T) {
	tmpDir := t.TempDir()
	d := NewFeatureDownloader(tmpDir, slog.Default())

	// Create a fake entry.
	cacheDir := d.cachePathFor("test-ref")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "install.sh"), []byte("ok"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := d.ClearCache(); err != nil {
		t.Fatalf("ClearCache: %v", err)
	}

	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("expected cache dir to be removed, stat err: %v", err)
	}
}

func TestSortFeatures(t *testing.T) {
	t.Run("no dependencies", func(t *testing.T) {
		features := []*ResolvedFeature{
			{Metadata: FeatureMetadata{ID: "a"}},
			{Metadata: FeatureMetadata{ID: "b"}},
			{Metadata: FeatureMetadata{ID: "c"}},
		}
		sorted := SortFeatures(features)
		ids := featureIDs(sorted)
		expected := "a,b,c"
		if ids != expected {
			t.Errorf("expected %q, got %q", expected, ids)
		}
	})

	t.Run("linear dependency chain", func(t *testing.T) {
		// c depends on b, b depends on a  =>  a, b, c
		features := []*ResolvedFeature{
			{Metadata: FeatureMetadata{ID: "c", InstallsAfter: []string{"b"}}},
			{Metadata: FeatureMetadata{ID: "a"}},
			{Metadata: FeatureMetadata{ID: "b", InstallsAfter: []string{"a"}}},
		}
		sorted := SortFeatures(features)
		ids := featureIDs(sorted)
		expected := "a,b,c"
		if ids != expected {
			t.Errorf("expected %q, got %q", expected, ids)
		}
	})

	t.Run("dependency on feature not in set is ignored", func(t *testing.T) {
		features := []*ResolvedFeature{
			{Metadata: FeatureMetadata{ID: "b", InstallsAfter: []string{"external"}}},
			{Metadata: FeatureMetadata{ID: "a"}},
		}
		sorted := SortFeatures(features)
		// Both have zero in-degree so original order is preserved.
		ids := featureIDs(sorted)
		expected := "b,a"
		if ids != expected {
			t.Errorf("expected %q, got %q", expected, ids)
		}
	})

	t.Run("single feature", func(t *testing.T) {
		features := []*ResolvedFeature{
			{Metadata: FeatureMetadata{ID: "only"}},
		}
		sorted := SortFeatures(features)
		if len(sorted) != 1 || sorted[0].Metadata.ID != "only" {
			t.Errorf("single feature sort failed")
		}
	})

	t.Run("nil input", func(t *testing.T) {
		sorted := SortFeatures(nil)
		if sorted != nil {
			t.Errorf("expected nil for nil input")
		}
	})
}

// indexOf returns the position of the feature with the given ID in the
// sorted slice, or -1 if absent.
func indexOf(features []*ResolvedFeature, id string) int {
	for i, f := range features {
		if f.Metadata.ID == id {
			return i
		}
	}
	return -1
}

// TestSortFeatures_CommonUtilsFirst guards the install-order blocker: the
// agent user (UID 1001) is created by common-utils, and every other feature's
// install.sh assumes it exists (claude-code/github-cli run `su agent`). Even
// when features arrive in the broken alphabetical order the provisioner used
// to hand us — and even when a feature like claude-code declares no dependency
// at all — common-utils must install first.
func TestSortFeatures_CommonUtilsFirst(t *testing.T) {
	features := []*ResolvedFeature{
		// Alphabetically "claude-code" sorts before "common-utils" (the bug);
		// it declares no installsAfter, so only the common-utils-first rule
		// can rescue it.
		{Ref: "ghcr.io/devcontainers-extra/features/claude-code:1", Metadata: FeatureMetadata{ID: "claude-code"}},
		{Ref: "ghcr.io/devcontainers/features/common-utils:2", Metadata: FeatureMetadata{ID: "common-utils"}},
		{Ref: "ghcr.io/devcontainers/features/github-cli:1", Metadata: FeatureMetadata{ID: "github-cli", InstallsAfter: []string{"ghcr.io/devcontainers/features/common-utils"}}},
	}
	sorted := SortFeatures(features)
	if got := featureIDs(sorted); len(sorted) != 3 {
		t.Fatalf("expected 3 features, got %d (%q)", len(sorted), got)
	}
	cu := indexOf(sorted, "common-utils")
	if cu != 0 {
		t.Fatalf("common-utils must install first, got order %q", featureIDs(sorted))
	}
	if cu > indexOf(sorted, "claude-code") || cu > indexOf(sorted, "github-cli") {
		t.Fatalf("common-utils must precede dependents, got order %q", featureIDs(sorted))
	}
}

// TestSortFeatures_InstallsAfterFullRefMatches isolates the topological-sort
// defect: upstream features express installsAfter as full OCI refs while a
// feature's own Metadata.ID is the short leaf id. The dependency edge must
// form despite that mismatch. No common-utils here, so this exercises Fix B
// (ref↔id normalization) independently of the common-utils-first convention.
func TestSortFeatures_InstallsAfterFullRefMatches(t *testing.T) {
	features := []*ResolvedFeature{
		{Ref: "ghcr.io/devcontainers/features/github-cli:1", Metadata: FeatureMetadata{ID: "github-cli", InstallsAfter: []string{"ghcr.io/devcontainers/features/git"}}},
		{Ref: "ghcr.io/devcontainers/features/git:1", Metadata: FeatureMetadata{ID: "git"}},
	}
	sorted := SortFeatures(features)
	if indexOf(sorted, "git") > indexOf(sorted, "github-cli") {
		t.Fatalf("git must install before github-cli (installsAfter full ref), got order %q", featureIDs(sorted))
	}
}

// TestSortFeatures_NoCycleWhenExplicitDep verifies the implicit
// common-utils-first edge composes cleanly with an explicit installsAfter on
// common-utils: no duplicate emission, no cycle fallback, all features present.
func TestSortFeatures_NoCycleWhenExplicitDep(t *testing.T) {
	features := []*ResolvedFeature{
		{Ref: "ghcr.io/devcontainers/features/common-utils:2", Metadata: FeatureMetadata{ID: "common-utils"}},
		{Ref: "ghcr.io/devcontainers/features/github-cli:1", Metadata: FeatureMetadata{ID: "github-cli", InstallsAfter: []string{"ghcr.io/devcontainers/features/common-utils"}}},
		{Ref: "ghcr.io/devcontainers/features/node:1", Metadata: FeatureMetadata{ID: "node"}},
	}
	sorted := SortFeatures(features)
	if len(sorted) != 3 {
		t.Fatalf("expected all 3 features, got %d (%q)", len(sorted), featureIDs(sorted))
	}
	seen := map[string]bool{}
	for _, f := range sorted {
		if seen[f.Metadata.ID] {
			t.Fatalf("feature %q emitted twice: %q", f.Metadata.ID, featureIDs(sorted))
		}
		seen[f.Metadata.ID] = true
	}
	if indexOf(sorted, "common-utils") != 0 {
		t.Fatalf("common-utils must be first, got order %q", featureIDs(sorted))
	}
	if indexOf(sorted, "common-utils") > indexOf(sorted, "github-cli") {
		t.Fatalf("common-utils must precede github-cli, got order %q", featureIDs(sorted))
	}
}

func TestReadMetadata(t *testing.T) {
	dir := t.TempDir()

	meta := FeatureMetadata{
		ID:          "test-feature",
		Version:     "1.0.0",
		Name:        "Test Feature",
		Description: "A test feature",
		Options: map[string]any{
			"version": map[string]any{
				"type":    "string",
				"default": "latest",
			},
		},
		ContainerEnv: map[string]string{
			"MY_VAR": "value",
		},
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "devcontainer-feature.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readMetadata(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.ID != "test-feature" {
		t.Errorf("expected ID %q, got %q", "test-feature", got.ID)
	}
	if got.ContainerEnv["MY_VAR"] != "value" {
		t.Errorf("expected container env MY_VAR=value")
	}
}

func TestReadMetadataMissing(t *testing.T) {
	dir := t.TempDir()
	_, err := readMetadata(dir)
	if err == nil {
		t.Fatal("expected error for missing metadata file")
	}
}

func TestExtractTarGz_Normal(t *testing.T) {
	destDir := t.TempDir()

	// Build a tar.gz with two normal files and a subdirectory.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Add a directory entry.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeDir,
		Name:     "subdir/",
		Mode:     0o755,
	}); err != nil {
		t.Fatal(err)
	}

	// Add a file at the root level.
	rootContent := []byte("#!/bin/sh\necho hello")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "install.sh",
		Mode:     0o755,
		Size:     int64(len(rootContent)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(rootContent); err != nil {
		t.Fatal(err)
	}

	// Add a file inside the subdirectory.
	subContent := []byte(`{"id":"test"}`)
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "subdir/meta.json",
		Mode:     0o644,
		Size:     int64(len(subContent)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(subContent); err != nil {
		t.Fatal(err)
	}

	tw.Close()

	if err := extractTarGz(&buf, destDir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// Verify root file.
	data, err := os.ReadFile(filepath.Join(destDir, "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	if string(data) != string(rootContent) {
		t.Errorf("install.sh content = %q, want %q", data, rootContent)
	}

	// Verify subdirectory file.
	data, err = os.ReadFile(filepath.Join(destDir, "subdir", "meta.json"))
	if err != nil {
		t.Fatalf("read subdir/meta.json: %v", err)
	}
	if string(data) != string(subContent) {
		t.Errorf("subdir/meta.json content = %q, want %q", data, subContent)
	}
}

// TestExtractTarGz_CumulativeBomb pins the M24 cumulative-size guard.
// The per-entry 50 MB cap alone doesn't stop a tar that streams many
// entries that each individually pass the per-file limit but together
// blow past the 500 MB total cap. We use small entries here (each
// 1 MB of zeros) and claim 600 MB total -- the guard must trip
// before the extraction completes the 600th entry. The check fires
// on the FIRST entry that would push the running total over the cap,
// so we don't actually need to stream gigabytes.
func TestExtractTarGz_CumulativeBomb(t *testing.T) {
	destDir := t.TempDir()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	const oneMB = 1 << 20
	chunk := bytes.Repeat([]byte{0}, oneMB)
	// 500 entries × 1 MB lands exactly at the cap; the 501st must
	// push over. We write 501 to confirm the rejection happens
	// inside the loop, not after.
	for i := 0; i < 501; i++ {
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     fmt.Sprintf("entry-%04d.bin", i),
			Mode:     0o644,
			Size:     int64(oneMB),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()

	err := extractTarGz(&buf, destDir)
	if err == nil {
		t.Fatal("expected cumulative-size error, got nil")
	}
	if !strings.Contains(err.Error(), "cumulative size cap") {
		t.Errorf("error = %v, want substring 'cumulative size cap'", err)
	}
}

func TestExtractTarGz_PathTraversal(t *testing.T) {
	destDir := t.TempDir()

	// Build a tar.gz with a path-traversal entry and a normal entry.
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Malicious entry: tries to escape destDir.
	malicious := []byte("pwned")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "../../etc/passwd",
		Mode:     0o644,
		Size:     int64(len(malicious)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(malicious); err != nil {
		t.Fatal(err)
	}

	// Another traversal variant.
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "../escape.txt",
		Mode:     0o644,
		Size:     int64(len(malicious)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(malicious); err != nil {
		t.Fatal(err)
	}

	// Normal entry that should be extracted.
	safe := []byte("safe content")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "safe.txt",
		Mode:     0o644,
		Size:     int64(len(safe)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(safe); err != nil {
		t.Fatal(err)
	}

	tw.Close()

	if err := extractTarGz(&buf, destDir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// The malicious entries should have been skipped.
	if _, err := os.Stat(filepath.Join(destDir, "..", "escape.txt")); !os.IsNotExist(err) {
		t.Error("path traversal entry ../escape.txt should not have been extracted")
	}

	// The safe file should exist.
	data, err := os.ReadFile(filepath.Join(destDir, "safe.txt"))
	if err != nil {
		t.Fatalf("read safe.txt: %v", err)
	}
	if string(data) != string(safe) {
		t.Errorf("safe.txt content = %q, want %q", data, safe)
	}
}

func TestExtractTarGz_Subdirectories(t *testing.T) {
	destDir := t.TempDir()

	// Build a tar.gz with nested directories (no explicit dir entries — tests implicit mkdir).
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	content := []byte("deep file")
	if err := tw.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "a/b/c/deep.txt",
		Mode:     0o644,
		Size:     int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}

	tw.Close()

	if err := extractTarGz(&buf, destDir); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}

	// Verify the nested file was created.
	data, err := os.ReadFile(filepath.Join(destDir, "a", "b", "c", "deep.txt"))
	if err != nil {
		t.Fatalf("read deep.txt: %v", err)
	}
	if string(data) != string(content) {
		t.Errorf("deep.txt content = %q, want %q", data, content)
	}

	// Verify intermediate directories exist.
	info, err := os.Stat(filepath.Join(destDir, "a", "b"))
	if err != nil {
		t.Fatalf("stat a/b: %v", err)
	}
	if !info.IsDir() {
		t.Error("a/b should be a directory")
	}
}

// helpers

func featureIDs(features []*ResolvedFeature) string {
	ids := make([]string, len(features))
	for i, f := range features {
		ids[i] = f.Metadata.ID
	}
	result := ""
	for i, id := range ids {
		if i > 0 {
			result += ","
		}
		result += id
	}
	return result
}

// TestFeatureMetadataParsesRuntimeRequirements verifies the new privileged,
// capAdd, mounts, and postCreateCommand fields round-trip through JSON.
func TestFeatureMetadataParsesRuntimeRequirements(t *testing.T) {
	raw := []byte(`{
		"id": "docker-in-docker",
		"version": "2.0.0",
		"name": "Docker-in-Docker",
		"privileged": true,
		"init": true,
		"capAdd": ["SYS_ADMIN"],
		"securityOpt": ["seccomp=unconfined"],
		"mounts": [
			{"source": "/var/run/docker.sock", "target": "/var/run/docker.sock", "type": "bind"}
		],
		"containerEnv": {"DOCKER_HOST": "unix:///var/run/docker.sock"},
		"postCreateCommand": "dockerd-rootless.sh &"
	}`)

	var meta FeatureMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Inverted post-F-011: feature-declared host-elevation requests are
	// now stripped at parse time. The fixture asks for privileged=true,
	// SYS_ADMIN, and seccomp=unconfined — none of which an untrusted OCI
	// feature should be able to grant itself.
	if meta.Privileged {
		t.Errorf("F-011: feature-supplied privileged=true must be stripped at parse time")
	}
	if !meta.Init {
		t.Errorf("expected init=true (Init is not part of the host-elevation strip)")
	}
	if len(meta.CapAdd) != 0 {
		t.Errorf("F-011: feature-supplied SYS_ADMIN must be filtered out; got %v", meta.CapAdd)
	}
	if len(meta.SecurityOpt) != 0 {
		t.Errorf("F-011: feature-supplied seccomp=unconfined must be dropped; got %v", meta.SecurityOpt)
	}
	if len(meta.Mounts) != 1 {
		t.Fatalf("mounts len = %d, want 1", len(meta.Mounts))
	}
	if meta.Mounts[0].Source != "/var/run/docker.sock" {
		t.Errorf("mount source = %q", meta.Mounts[0].Source)
	}
	if meta.ContainerEnv["DOCKER_HOST"] == "" {
		t.Errorf("expected DOCKER_HOST env")
	}
	// PostCreateCommand is any — NormalizeCommand should turn it into []string.
	cmds := NormalizeCommand(meta.PostCreateCommand)
	if len(cmds) != 1 || cmds[0] != "dockerd-rootless.sh &" {
		t.Errorf("postCreate cmds = %v", cmds)
	}
}

// TestAggregateFeatureRequirementsPrivilegedOR verifies that if any feature
// declares privileged:true, the aggregate is also privileged.
func TestAggregateFeatureRequirementsPrivilegedOR(t *testing.T) {
	p := &Provisioner{}
	features := []*ResolvedFeature{
		{Metadata: FeatureMetadata{ID: "a"}}, // not privileged
		{Metadata: FeatureMetadata{ID: "b", Privileged: true}},
		{Metadata: FeatureMetadata{ID: "c"}},
	}
	req := p.aggregateFeatureRequirements(features, nil)
	if !req.Privileged {
		t.Errorf("expected aggregate privileged=true when any feature is privileged")
	}
}

// TestAggregateFeatureRequirementsRootEnvWins verifies that root-level
// containerEnv overrides feature-declared values for the same key.
func TestAggregateFeatureRequirementsRootEnvWins(t *testing.T) {
	p := &Provisioner{}
	features := []*ResolvedFeature{
		{Metadata: FeatureMetadata{
			ID:           "a",
			ContainerEnv: map[string]string{"TZ": "UTC", "FEATURE_VAR": "from-feature"},
		}},
		{Metadata: FeatureMetadata{
			ID:           "b",
			ContainerEnv: map[string]string{"TZ": "Europe/Prague"}, // should lose to feature "a" (first wins among features)
		}},
	}
	rootEnv := map[string]string{
		"TZ":       "America/New_York", // overrides all features
		"ROOT_VAR": "root-only",
	}
	req := p.aggregateFeatureRequirements(features, rootEnv)

	if got := req.ContainerEnv["TZ"]; got != "America/New_York" {
		t.Errorf("root-level TZ should win: got %q, want America/New_York", got)
	}
	if got := req.ContainerEnv["ROOT_VAR"]; got != "root-only" {
		t.Errorf("root-only var missing: got %q", got)
	}
	if got := req.ContainerEnv["FEATURE_VAR"]; got != "from-feature" {
		t.Errorf("feature var should survive when root doesn't redeclare: got %q", got)
	}
}

// TestAggregateFeatureRequirementsConcatsCapsAndMounts verifies capAdd,
// securityOpt, and mounts aggregate by concatenation across features.
func TestAggregateFeatureRequirementsConcatsCapsAndMounts(t *testing.T) {
	p := &Provisioner{}
	features := []*ResolvedFeature{
		{Metadata: FeatureMetadata{
			ID:     "a",
			CapAdd: []string{"SYS_ADMIN"},
			Mounts: []FeatureMount{{Source: "/a", Target: "/a", Type: "bind"}},
		}},
		{Metadata: FeatureMetadata{
			ID:          "b",
			CapAdd:      []string{"NET_ADMIN"},
			SecurityOpt: []string{"seccomp=unconfined"},
			Mounts:      []FeatureMount{{Source: "/b", Target: "/b", Type: "bind"}},
		}},
	}
	req := p.aggregateFeatureRequirements(features, nil)
	if len(req.CapAdd) != 2 {
		t.Errorf("capAdd len = %d, want 2: %v", len(req.CapAdd), req.CapAdd)
	}
	if len(req.SecurityOpt) != 1 {
		t.Errorf("securityOpt len = %d, want 1", len(req.SecurityOpt))
	}
	if len(req.Mounts) != 2 {
		t.Errorf("mounts len = %d, want 2", len(req.Mounts))
	}
}
