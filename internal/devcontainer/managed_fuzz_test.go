package devcontainer

import (
	"bytes"
	"strings"
	"testing"
)

func FuzzManagedNativeLock(f *testing.F) {
	f.Add("lockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\"\n", "codex")
	f.Add("lockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\"\nversion = \"0.159.0\"\n", "codex")
	f.Fuzz(func(t *testing.T, raw, tool string) {
		if len(raw) > maxMiseLockFileBytes || len(tool) > 256 {
			return
		}
		bundle := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": raw}}
		version, err := LockedToolVersion(bundle, tool)
		if err != nil {
			return
		}
		if bundle.Validate() != nil || !toolVersionPattern.MatchString(version) {
			t.Fatal("invalid native lock evidence admitted")
		}
		// A second canonical tool entry must always make the lock ambiguous.
		duplicated := raw + "\n[[tools." + tool + "]]\nversion = \"" + version + "\"\n"
		if _, err := LockedToolVersion(&MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": duplicated}}, tool); err == nil {
			t.Fatal("duplicate native lock entry admitted")
		}
	})
}

func FuzzManagedToolchainArchive(f *testing.F) {
	f.Add(toolchainArchive(f, map[string]string{"schema": "1", "codex.version": "codex-cli 0.160.0", "codex.status": "0", "codex.path": "/opt/mise/data/shims/codex"}))
	f.Add(toolchainArchive(f, map[string]string{"schema": "1", "../outside": "untrusted"}))
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 1<<20 {
			return
		}
		inventory, err := parseToolchainArchive(bytes.NewReader(raw), []string{"codex"})
		if err != nil {
			return
		}
		if inventory == nil || inventory.SchemaVersion != 1 || len(inventory.Tools) != 1 {
			t.Fatal("invalid inventory shape")
		}
		tool := inventory.Tools[0]
		if tool.Version != "" && !toolVersionPattern.MatchString(tool.Version) {
			t.Fatal("noncanonical observed version")
		}
		if tool.ManagedPath != "" && !managedToolPath("codex", tool.ManagedVersion, tool.ManagedPath) {
			t.Fatal("untrusted managed path")
		}
	})
}

func FuzzManagedObservedVersion(f *testing.F) {
	f.Add("codex", "codex-cli 0.160.0")
	f.Add("claude", "SECRET=2.1.0")
	f.Fuzz(func(t *testing.T, tool, raw string) {
		if len(raw) > 1<<20 || len(tool) > 256 {
			return
		}
		version := ObservedToolVersion(tool, raw)
		if version != "" && (!toolVersionPattern.MatchString(version) || strings.ContainsAny(version, "\r\n")) {
			t.Fatal("untrusted output admitted")
		}
	})
}
