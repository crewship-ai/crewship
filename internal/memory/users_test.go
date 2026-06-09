package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The user model reuses UserSlug for its filename derivation, so the
// slug invariants (determinism, workspace isolation, fail-closed on
// empty input) are already exercised in peers_test.go. These tests
// focus on the user-model-specific read/write/delete/list surface.

func TestUserModel_WriteReadDeleteRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}

	// Missing → empty + nil. A user the workspace has no model for is
	// the common case, not an error.
	body, err := LoadUserModel(p, "u1", "ws1")
	if err != nil || body != "" {
		t.Fatalf("expected empty miss; got body=%q err=%v", body, err)
	}

	want := "Prefers concise answers. Works in UTC+1."
	if err := WriteUserModel(p, "u1", "ws1", want); err != nil {
		t.Fatalf("WriteUserModel: %v", err)
	}
	body, err = LoadUserModel(p, "u1", "ws1")
	if err != nil || body != want {
		t.Errorf("round-trip mismatch: got %q (err=%v)", body, err)
	}

	// Listing must surface the derived hash, never the raw user_id.
	slugs, err := ListUserModelSlugs(p)
	if err != nil {
		t.Fatalf("ListUserModelSlugs: %v", err)
	}
	wantSlug := UserSlug("u1", "ws1")
	if len(slugs) != 1 || slugs[0] != wantSlug {
		t.Errorf("expected [%q], got %v", wantSlug, slugs)
	}
	for _, s := range slugs {
		if strings.Contains(s, "u1") || strings.Contains(s, "ws1") {
			t.Errorf("slug %q leaks raw user_id/workspace_id", s)
		}
	}

	// Delete is idempotent — call twice, no error.
	if err := DeleteUserModel(p, "u1", "ws1"); err != nil {
		t.Errorf("DeleteUserModel: %v", err)
	}
	if err := DeleteUserModel(p, "u1", "ws1"); err != nil {
		t.Errorf("DeleteUserModel idempotent failed: %v", err)
	}
	body, _ = LoadUserModel(p, "u1", "ws1")
	if body != "" {
		t.Errorf("post-delete read should be empty; got %q", body)
	}
}

func TestUserModel_LoadBySlug(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "u1", "ws1", "hint body"); err != nil {
		t.Fatalf("WriteUserModel: %v", err)
	}
	slug := UserSlug("u1", "ws1")
	body, err := LoadUserModelBySlug(p, slug)
	if err != nil || body != "hint body" {
		t.Errorf("LoadUserModelBySlug mismatch: got %q err=%v", body, err)
	}
	// Empty slug fails closed to ("", nil).
	if body, err := LoadUserModelBySlug(p, ""); err != nil || body != "" {
		t.Errorf("expected empty slug → empty; got %q err=%v", body, err)
	}
}

func TestUserModel_CapEnforced(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	big := strings.Repeat("x", UserModelCapBytes+1)
	if err := WriteUserModel(p, "u1", "ws1", big); err == nil {
		t.Errorf("expected cap rejection on oversize write")
	}
	atCap := strings.Repeat("y", UserModelCapBytes)
	if err := WriteUserModel(p, "u1", "ws1", atCap); err != nil {
		t.Errorf("at-cap write rejected: %v", err)
	}
}

func TestUserModel_EmptyRejected(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "u1", "ws1", "   "); err == nil {
		t.Errorf("expected empty-content rejection")
	}
}

func TestUserModel_EmptyUserIDRejected(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "", "ws1", "anything"); err == nil {
		t.Errorf("expected error when user_id is empty")
	}
}

func TestUserModel_DeleteBySlug(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "u1", "ws1", "body"); err != nil {
		t.Fatalf("WriteUserModel: %v", err)
	}
	slug := UserSlug("u1", "ws1")
	if err := DeleteUserModelBySlug(p, slug); err != nil {
		t.Errorf("DeleteUserModelBySlug: %v", err)
	}
	if body, _ := LoadUserModel(p, "u1", "ws1"); body != "" {
		t.Errorf("expected purge; got %q", body)
	}
	// Empty slug is a no-op.
	if err := DeleteUserModelBySlug(p, ""); err != nil {
		t.Errorf("empty slug delete should be nil; got %v", err)
	}
	// Empty user_id delete is a no-op too.
	if err := DeleteUserModel(p, "", "ws1"); err != nil {
		t.Errorf("empty user delete should be nil; got %v", err)
	}
}

// List over an absent directory returns nil, nil — the worker sweep
// reconciles disk vs DB and a workspace with no models yet is normal.
func TestUserModel_ListMissingDir(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir} // UsersDir() does not exist yet
	slugs, err := ListUserModelSlugs(p)
	if err != nil || slugs != nil {
		t.Errorf("expected nil,nil for missing dir; got %v err=%v", slugs, err)
	}
}

func TestUserModel_MultiUserList(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	for _, u := range []string{"alice", "bob", "carol"} {
		if err := WriteUserModel(p, u, "ws1", u+" model"); err != nil {
			t.Fatalf("write %s: %v", u, err)
		}
	}
	slugs, err := ListUserModelSlugs(p)
	if err != nil {
		t.Fatalf("ListUserModelSlugs: %v", err)
	}
	if len(slugs) != 3 {
		t.Errorf("expected 3 slugs, got %d: %v", len(slugs), slugs)
	}
}

// WriteUserModel with empty workspace fails closed (slug == "").
func TestUserModel_EmptyWorkspaceRejected(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "u1", "", "anything"); err == nil {
		t.Errorf("expected error when workspace_id is empty")
	}
}

// Empty slug short-circuits the read path (no file touch) → ("", nil).
func TestUserModel_LoadEmptySlugShortCircuit(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if body, err := LoadUserModel(p, "", "ws1"); err != nil || body != "" {
		t.Errorf("expected empty user → empty,nil; got %q,%v", body, err)
	}
	if body, err := LoadUserModel(p, "u1", ""); err != nil || body != "" {
		t.Errorf("expected empty workspace → empty,nil; got %q,%v", body, err)
	}
}

// A model path that is actually a directory surfaces a real read error
// (not ENOENT), exercising the non-ENOENT branch of loadUserModelFile.
func TestUserModel_LoadDirIsError(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	slug := UserSlug("u1", "ws1")
	if err := os.MkdirAll(p.ModelPath(slug), 0o755); err != nil {
		t.Fatalf("mkdir model-as-dir: %v", err)
	}
	if _, err := LoadUserModelBySlug(p, slug); err == nil {
		t.Errorf("expected read error when model path is a directory")
	}
}

// mkdir failure: SharedDir's users/ parent is occupied by a regular
// file, so MkdirAll inside WriteUserModel fails.
func TestUserModel_WriteMkdirError(t *testing.T) {
	dir := t.TempDir()
	// Make "users" a regular file so UsersDir() can't be created.
	if err := os.WriteFile(filepath.Join(dir, "users"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	p := UserModelPaths{SharedDir: dir}
	if err := WriteUserModel(p, "u1", "ws1", "body"); err == nil {
		t.Errorf("expected mkdir error when users/ is a file")
	}
}

// Remove failure: the model path is a non-empty directory, so
// os.Remove returns a non-ENOENT error, exercising that branch.
func TestUserModel_DeleteRemoveError(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	slug := UserSlug("u1", "ws1")
	mp := p.ModelPath(slug)
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mp, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if err := DeleteUserModelBySlug(p, slug); err == nil {
		t.Errorf("expected remove error for non-empty directory at model path")
	}
}

// ListUserModelSlugs must skip subdirectories and non-.md files.
func TestUserModel_ListSkipsNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	p := UserModelPaths{SharedDir: dir}
	if err := os.MkdirAll(p.UsersDir(), 0o755); err != nil {
		t.Fatalf("mkdir users: %v", err)
	}
	// One valid .md, one .txt, one subdir — only the .md counts.
	if err := os.WriteFile(filepath.Join(p.UsersDir(), "abc.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.UsersDir(), "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write txt: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(p.UsersDir(), "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	slugs, err := ListUserModelSlugs(p)
	if err != nil {
		t.Fatalf("ListUserModelSlugs: %v", err)
	}
	if len(slugs) != 1 || slugs[0] != "abc" {
		t.Errorf("expected [abc]; got %v", slugs)
	}
}
