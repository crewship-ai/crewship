package devcontainer

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
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

	// Create a fake cache entry.
	cacheDir := d.cachePathFor(ref)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "install.sh"), []byte("#!/bin/sh\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Now it should be cached.
	if !d.IsCached(ref) {
		t.Fatal("expected cached after creating install.sh")
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
	if !meta.Privileged {
		t.Errorf("expected privileged=true")
	}
	if !meta.Init {
		t.Errorf("expected init=true")
	}
	if len(meta.CapAdd) != 1 || meta.CapAdd[0] != "SYS_ADMIN" {
		t.Errorf("capAdd = %v, want [SYS_ADMIN]", meta.CapAdd)
	}
	if len(meta.SecurityOpt) != 1 || meta.SecurityOpt[0] != "seccomp=unconfined" {
		t.Errorf("securityOpt = %v", meta.SecurityOpt)
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
