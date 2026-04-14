package devcontainer

import (
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
			{Metadata: FeatureMetadata{ID: "c", InstallsAfter: []InstallsAfter{{ID: "b"}}}},
			{Metadata: FeatureMetadata{ID: "a"}},
			{Metadata: FeatureMetadata{ID: "b", InstallsAfter: []InstallsAfter{{ID: "a"}}}},
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
			{Metadata: FeatureMetadata{ID: "b", InstallsAfter: []InstallsAfter{{ID: "external"}}}},
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
