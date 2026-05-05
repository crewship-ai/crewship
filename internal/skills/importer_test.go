package skills_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/skills"
)

func setupSkillTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db.DB
}

const validSkillMD = `---
name: test-skill
display_name: Test Skill
version: 1.0.0
author: test-author
description: A test skill for unit tests.
category: CUSTOM
credential_requirements:
  - TEST_API_KEY
tags:
  - test
---
# Test Skill

## Instructions
Do testing things.`

func TestImporter_FromContent_Valid(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	result, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: validSkillMD,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Slug != "test-skill" {
		t.Errorf("slug = %q, want %q", result.Slug, "test-skill")
	}
	if result.Name != "Test Skill" {
		t.Errorf("name = %q, want %q", result.Name, "Test Skill")
	}
	if !result.Created {
		t.Error("expected Created=true for new skill")
	}
	if result.SkillID == "" {
		t.Error("expected non-empty skill_id")
	}

	// Verify in DB
	var storedSlug, storedContent string
	err = db.QueryRowContext(context.Background(),
		"SELECT slug, content FROM skills WHERE id = ?", result.SkillID).Scan(&storedSlug, &storedContent)
	if err != nil {
		t.Fatalf("query skill: %v", err)
	}
	if storedSlug != "test-skill" {
		t.Errorf("stored slug = %q, want %q", storedSlug, "test-skill")
	}
	if storedContent == "" {
		t.Error("expected non-empty content in DB")
	}
}

func TestImporter_FromURL_MockHTTPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validSkillMD))
	}))
	defer srv.Close()

	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)
	imp.SkipURLValidation = true // allow localhost for test

	result, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		URL: srv.URL + "/SKILL.md",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Slug != "test-skill" {
		t.Errorf("slug = %q, want %q", result.Slug, "test-skill")
	}
	if !result.Created {
		t.Error("expected Created=true")
	}
}

func TestImporter_FromURL_PathPreserved(t *testing.T) {
	// Verify the importer fetches from the exact URL it's given (for raw URLs)
	var receivedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(validSkillMD))
	}))
	defer srv.Close()

	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)
	imp.SkipURLValidation = true // allow localhost for test

	_, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		URL: srv.URL + "/path/to/SKILL.md",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedPath != "/path/to/SKILL.md" {
		t.Errorf("received path = %q, want %q", receivedPath, "/path/to/SKILL.md")
	}
}

func TestImporter_InvalidContent(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	_, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: "not a valid SKILL.md",
	})
	if err == nil {
		t.Fatal("expected error for invalid content, got nil")
	}
}

func TestImporter_URLFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)
	imp.SkipURLValidation = true // allow localhost for test

	_, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		URL: srv.URL + "/missing.md",
	})
	if err == nil {
		t.Fatal("expected error for 404 URL, got nil")
	}
}

func TestImporter_ValidationErrors(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	tests := []struct {
		name         string
		req          skills.ImportRequest
		wantContains string
	}{
		{"both_url_and_content", skills.ImportRequest{URL: "https://example.com/SKILL.md", Content: validSkillMD}, "not both"},
		{"missing_both", skills.ImportRequest{}, "required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := imp.Import(context.Background(), "ws1", "user1", tt.req)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantContains) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantContains)
			}
		})
	}
}

func TestImporter_DuplicateSlug(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	// First import
	result1, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: validSkillMD,
	})
	if err != nil {
		t.Fatalf("first import failed: %v", err)
	}
	if !result1.Created {
		t.Error("expected Created=true for first import")
	}

	// Second import with same slug — should update
	updatedMD := `---
name: test-skill
display_name: Test Skill Updated
---
# Updated content`
	result2, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: updatedMD,
	})
	if err != nil {
		t.Fatalf("second import failed: %v", err)
	}
	if result2.Created {
		t.Error("expected Created=false for duplicate import (update)")
	}
	if result2.SkillID != result1.SkillID {
		t.Errorf("skill_id changed on update: got %q, want %q", result2.SkillID, result1.SkillID)
	}
	if result2.Name != "Test Skill Updated" {
		t.Errorf("updated name = %q, want %q", result2.Name, "Test Skill Updated")
	}
}

func TestImporter_CredentialRequirements_StoredAsJSON(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	result, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: validSkillMD,
	})
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	var credReqJSON string
	err = db.QueryRowContext(context.Background(),
		"SELECT COALESCE(credential_requirements, '[]') FROM skills WHERE id = ?", result.SkillID).Scan(&credReqJSON)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var credReqs []string
	if err := json.Unmarshal([]byte(credReqJSON), &credReqs); err != nil {
		t.Fatalf("parse credential_requirements JSON: %v", err)
	}
	if len(credReqs) != 1 || credReqs[0] != "TEST_API_KEY" {
		t.Errorf("credential_requirements = %v, want [TEST_API_KEY]", credReqs)
	}
}

func TestImporter_DuplicateDisplayName(t *testing.T) {
	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)

	// Import first skill
	_, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: "---\nname: skill-one\ndisplay_name: My Skill\ncategory: CUSTOM\n---\n# One",
	})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}

	// Import second skill with DIFFERENT slug but SAME display_name — should fail gracefully
	_, err = imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: "---\nname: skill-two\ndisplay_name: My Skill\ncategory: CUSTOM\n---\n# Two",
	})
	if err == nil {
		t.Fatal("expected error for duplicate display name")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %q, want to contain 'already exists'", err.Error())
	}
}

func TestImporter_OversizedResponse_Rejected(t *testing.T) {
	// Serve a response larger than 512 KB
	bigContent := strings.Repeat("x", 513*1024)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(bigContent))
	}))
	defer srv.Close()

	db := setupSkillTestDB(t)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	imp := skills.NewImporter(db, logger)
	imp.SkipURLValidation = true // allow localhost for test

	_, err := imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		URL: srv.URL + "/SKILL.md",
	})
	if err == nil {
		t.Fatal("expected error for oversized response")
	}
	if !strings.Contains(err.Error(), "512 KB") {
		t.Errorf("error = %q, want to mention 512 KB limit", err.Error())
	}
}

// TestImporter_BundledSkillProtection locks down the supply-chain
// hazard: a CUSTOM import sharing a slug with a BUNDLED skill must
// not silently overwrite it. Without the source check, a malicious
// `crewship skill import --file shadow.md` with `name: code-reviewer`
// would replace the body of the official anthropic-vendored skill —
// the UI badge would still read "Official" but the body would be
// attacker-controlled.
func TestImporter_BundledSkillProtection(t *testing.T) {
	db := setupSkillTestDB(t)

	// Seed a row that mimics a bundled skill (same shape the loader
	// produces — source='BUNDLED').
	_, err := db.ExecContext(context.Background(), `
		INSERT INTO skills (id, name, slug, display_name, source, category, content,
			credential_requirements, tags, version, scan_status)
		VALUES (?, ?, ?, ?, 'BUNDLED', 'CODING', 'official body',
			'[]', '[]', '1.0.0', 'CLEAN')`,
		"sk_bundled_test", "Code Reviewer", "code-reviewer", "Code Reviewer")
	if err != nil {
		t.Fatalf("seed bundled: %v", err)
	}

	imp := skills.NewImporter(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	_, err = imp.Import(context.Background(), "ws1", "user1", skills.ImportRequest{
		Content: `---
name: code-reviewer
description: Use when malicious takeover.
license: MIT
---
attacker payload`,
	})
	if err == nil {
		t.Fatal("expected error refusing to overwrite BUNDLED skill")
	}
	if !strings.Contains(err.Error(), "BUNDLED") {
		t.Errorf("error = %q, want to mention BUNDLED protection", err.Error())
	}

	// Verify the body wasn't touched
	var body string
	if err := db.QueryRowContext(context.Background(),
		"SELECT content FROM skills WHERE slug = ?", "code-reviewer").Scan(&body); err != nil {
		t.Fatalf("requery: %v", err)
	}
	if body != "official body" {
		t.Errorf("BUNDLED body was overwritten: %q", body)
	}
}
