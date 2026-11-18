package devcontainer

import "testing"

func TestManagedLockedVersionRequiresCanonicalNativeEvidence(t *testing.T) {
	for _, raw := range []string{
		"opaque fixture lock", "lockfile_version = 3\n", "lockfile_version = 2\n[[tools.codex]]\nversion = \"0.160.0\"",
		"lockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\"\nversion = \"0.159.0\"",
		"lockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\"\n[[tools.codex]]\nversion = \"0.160.0\"",
		"lockfile_version = 3\ncomment = '''\n[[tools.codex]]\nversion = \"0.160.0\"\n'''",
	} {
		if _, err := LockedToolVersion(&MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": raw}}, "codex"); err == nil {
			t.Fatalf("ambiguous lock accepted: %s", raw)
		}
	}
	raw := "# native mise output\nlockfile_version = 3\n[[tools.codex]]\nversion = \"0.160.0\" # selected version\nbackend = \"aqua:openai/codex\"\n[tools.codex.\"platforms.linux-x64\"]\nchecksum = \"sha256:fixture\"\n[[tools.node]]\nversion = \"22.0.0\"\n"
	version, err := LockedToolVersion(&MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": raw}}, "codex")
	if err != nil || version != "0.160.0" {
		t.Fatalf("native version=%s %v", version, err)
	}
}

// Native mise2026.11.9 creates v4 locks; v4 adds npm provenance without
// changing the canonical tool/version tables needed by managed launch.
func TestManagedLockedVersionAcceptsNativeV4AndRejectsUnknownSchemas(t *testing.T) {
	for _, schema := range []string{"3", "4"} {
		raw := "lockfile_version = " + schema + "\n[[tools.codex]]\nversion = \"0.169.0\"\nbackend = \"aqua:openai/codex\"\nspecifiers = [\"0.169.0\"]\n[tools.codex.\"platforms.linux-x64\"]\nchecksum = \"sha256:synthetic-fixture\"\nurl = \"https://example.invalid/codex.tar.gz\"\n"
		got, err := LockedToolVersion(&MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": raw}}, "codex")
		if err != nil || got != "0.169.0" {
			t.Fatalf("native schema%s: version=%q err=%v", schema, got, err)
		}
	}
	for _, header := range []string{"lockfile_version = 5", "lockfile_version = 40", "lockfile_version = 0", "lockfile_version = 4\nlockfile_version = 3"} {
		raw := header + "\n[[tools.codex]]\nversion = \"0.169.0\"\n"
		if _, err := LockedToolVersion(&MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": raw}}, "codex"); err == nil {
			t.Fatalf("unsupported/ambiguous schema accepted: %s", header)
		}
	}
}
