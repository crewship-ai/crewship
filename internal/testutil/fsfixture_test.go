package testutil

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewMemFS verifies that the helper builds an in-memory filesystem
// populated with the given files, including auto-creating parent directories.
func TestNewMemFS(t *testing.T) {
	fs := NewMemFS(t, map[string]string{
		"AGENT.md":               "# Root agent memory",
		"daily/2026-04-10.md":    "work log entry",
		"notes/design/arch.md":   "architecture notes",
		"notes/design/tech.md":   "tech decisions",
		"skills/custom/SKILL.md": "skill definition",
	})

	// Each file must exist with exact content
	data, err := afero.ReadFile(fs, "AGENT.md")
	require.NoError(t, err)
	assert.Equal(t, "# Root agent memory", string(data))

	data, err = afero.ReadFile(fs, "daily/2026-04-10.md")
	require.NoError(t, err)
	assert.Equal(t, "work log entry", string(data))

	// Nested directories must be auto-created
	exists, err := afero.DirExists(fs, "notes/design")
	require.NoError(t, err)
	assert.True(t, exists, "nested directory should be auto-created")
}

// TestListFiles verifies ListFiles walks the fs and returns only files,
// not directories.
func TestListFiles(t *testing.T) {
	fs := NewMemFS(t, map[string]string{
		"a.md":          "",
		"sub/b.md":      "",
		"sub/deep/c.md": "",
	})

	files := ListFiles(t, fs, ".")

	assert.Len(t, files, 3, "should list exactly 3 files")
	assert.Contains(t, files, "a.md")
	assert.Contains(t, files, "sub/b.md")
	assert.Contains(t, files, "sub/deep/c.md")
}

// TestAssertFileContent verifies the helper succeeds on match and would
// fail (not tested directly) on mismatch.
func TestAssertFileContent(t *testing.T) {
	fs := NewMemFS(t, map[string]string{
		"readme.md": "# Crewship\n\nAgent orchestration platform.",
	})

	// Should not fail
	AssertFileContent(t, fs, "readme.md", "Crewship")
	AssertFileContent(t, fs, "readme.md", "orchestration")
}

// TestNewMemFS_Empty verifies that an empty fixture still returns a usable Fs.
func TestNewMemFS_Empty(t *testing.T) {
	fs := NewMemFS(t, nil)

	files := ListFiles(t, fs, ".")
	assert.Empty(t, files, "empty fixture should have zero files")

	// Should be able to write new files to it
	err := afero.WriteFile(fs, "new.txt", []byte("hello"), 0o644)
	require.NoError(t, err)

	data, err := afero.ReadFile(fs, "new.txt")
	require.NoError(t, err)
	assert.Equal(t, "hello", string(data))
}
