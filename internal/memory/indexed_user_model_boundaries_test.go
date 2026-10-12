package memory

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func indexedModelFixture(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	base := t.TempDir()
	for _, query := range []string{
		`INSERT INTO users(id,email) VALUES('u','model@example.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('w','Workspace','model-workspace')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','w','Crew','model-crew')`,
	} {
		if _, err := db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	name := filepath.Join(base, "crews", "crew", "shared", ".memory", "users", UserSlug("u", "w")+".md")
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("authoritative model"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO user_models(id,workspace_id,crew_id,user_id,user_slug,path,bytes) VALUES('model','w','crew','u',?,?,19)`, UserSlug("u", "w"), name); err != nil {
		t.Fatal(err)
	}
	return db, base, name
}

func TestIndexedUserModelFollowsIndexAndConsent(t *testing.T) {
	db, base, name := indexedModelFixture(t)
	body, err := ReadIndexedUserModel(t.Context(), db, base, "w", "u")
	if err != nil || body != "authoritative model" {
		t.Fatalf("indexed read: %q %v", body, err)
	}
	allowed, err := PersonalizationAllowed(t.Context(), db, "w", "u")
	if err != nil || !allowed {
		t.Fatalf("initial consent: %v %v", allowed, err)
	}
	for _, tc := range []struct{ name, query string }{
		{"opt out", `INSERT INTO user_peer_consent(user_id,workspace_id,opted_out) VALUES('u','w',1)`},
		{"delete crew", `UPDATE crews SET deleted_at='2026-01-01T00:00:00Z' WHERE id='crew'`},
		{"delete index", `DELETE FROM user_models WHERE id='model'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.ExecContext(t.Context(), tc.query); err != nil {
				t.Fatal(err)
			}
			body, err := ReadIndexedUserModel(t.Context(), db, base, "w", "u")
			if err != nil || body != "" {
				t.Fatalf("stale model returned: %q %v", body, err)
			}
			if _, err := os.Stat(name); err != nil {
				t.Fatal("test must retain stale physical file", err)
			}
			if tc.name == "opt out" {
				allowed, err := PersonalizationAllowed(t.Context(), db, "w", "u")
				if err != nil || allowed {
					t.Fatalf("opt out ignored: %v %v", allowed, err)
				}
				if _, err := db.ExecContext(t.Context(), `DELETE FROM user_peer_consent`); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "delete crew" {
				if _, err := db.ExecContext(t.Context(), `UPDATE crews SET deleted_at=NULL WHERE id='crew'`); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestIndexedUserModelRejectsUnsafeOrUnavailableFilesystem(t *testing.T) {
	for _, kind := range []string{"missing root", "missing directory", "directory is file", "directory symlink", "missing model", "model is directory", "model symlink", "oversized model"} {
		t.Run(kind, func(t *testing.T) {
			db, base, name := indexedModelFixture(t)
			switch kind {
			case "missing root":
				base = filepath.Join(base, "absent")
			case "missing directory":
				if err := os.RemoveAll(filepath.Join(base, "crews")); err != nil {
					t.Fatal(err)
				}
			case "directory is file", "directory symlink":
				crews := filepath.Join(base, "crews")
				if err := os.RemoveAll(crews); err != nil {
					t.Fatal(err)
				}
				if kind == "directory is file" {
					if err := os.WriteFile(crews, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Symlink(t.TempDir(), crews); err != nil {
						t.Fatal(err)
					}
				}
			case "missing model", "model is directory", "model symlink":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				if kind == "model is directory" {
					if err := os.Mkdir(name, 0700); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "model symlink" {
					outside := filepath.Join(t.TempDir(), "private")
					if err := os.WriteFile(outside, []byte("foreign private content"), 0600); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, name); err != nil {
						t.Fatal(err)
					}
				}
			case "oversized model":
				if err := os.WriteFile(name, []byte(strings.Repeat("x", UserModelCapBytes+1)), 0600); err != nil {
					t.Fatal(err)
				}
			}
			body, err := ReadIndexedUserModel(t.Context(), db, base, "w", "u")
			if err == nil || body != "" {
				t.Fatalf("unsafe model accepted: %q %v", body, err)
			}
		})
	}
}

func TestIndexedUserModelCannotCrossWorkspaceOrHideConsentStoreFailure(t *testing.T) {
	db, base, _ := indexedModelFixture(t)
	for _, ids := range [][3]string{{"", "w", "u"}, {base, "", "u"}, {base, "w", ""}, {base, "other", "u"}, {base, "w", "other"}} {
		body, err := ReadIndexedUserModel(t.Context(), db, ids[0], ids[1], ids[2])
		if err != nil || body != "" {
			t.Fatalf("foreign or incomplete identity read model: %q %v", body, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if body, err := ReadIndexedUserModel(t.Context(), db, base, "w", "u"); err == nil || body != "" {
		t.Fatalf("closed index accepted: %q %v", body, err)
	}
	if allowed, err := PersonalizationAllowed(t.Context(), db, "w", "u"); err == nil || allowed {
		t.Fatalf("closed consent store allowed personalization: %v %v", allowed, err)
	}
}
