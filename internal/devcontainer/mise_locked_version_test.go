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
