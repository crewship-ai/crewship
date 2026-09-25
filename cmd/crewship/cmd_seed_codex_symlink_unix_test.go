//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink named by SEED_CODEX_AUTH_FILE must be refused by the open
// itself (O_NOFOLLOW), not by a stat that can race with the swap.
func TestResolveSeedCodexLoginRefusesSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	content := `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"id","access_token":"access","refresh_token":"refresh","account_id":"account"}}`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "auth-link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlink creation refused: %v", err)
	}
	defer os.Remove(link)
	t.Setenv(seedCodexAuthFileEnv, link)
	if _, err := resolveSeedCodexLogin(); err == nil {
		t.Fatal("symlink named by SEED_CODEX_AUTH_FILE must be refused")
	}
}
