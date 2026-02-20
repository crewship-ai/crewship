package skills

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"log/slog"
	"net/http"
	"time"
)

// ImportRequest specifies a skill to import — either from a URL or pasted content.
// Exactly one of URL or Content must be non-empty.
type ImportRequest struct {
	URL     string
	Content string
}

// ImportResult is returned by a successful Import call.
type ImportResult struct {
	SkillID string `json:"skill_id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Created bool   `json:"created"`
}

// Importer fetches, parses, and upserts skills into the database.
type Importer struct {
	db     *sql.DB
	logger *slog.Logger
	client *http.Client
}

// NewImporter creates an Importer with a 30-second HTTP timeout.
func NewImporter(db *sql.DB, logger *slog.Logger) *Importer {
	return &Importer{
		db:     db,
		logger: logger,
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Import imports a skill from the given request and upserts it into the database.
// The workspaceID and userID parameters are accepted for API handler compatibility
// but currently unused — skills are global platform-wide resources (no workspace_id
// column in the skills table). They may be used for audit logging in the future.
func (imp *Importer) Import(ctx context.Context, _, _ string, req ImportRequest) (*ImportResult, error) {
	var content string

	switch {
	case req.Content != "":
		content = req.Content
	case req.URL != "":
		normalised, err := NormalizeSkillURL(req.URL)
		if err != nil {
			return nil, err
		}
		fetched, err := imp.fetchURL(ctx, normalised)
		if err != nil {
			return nil, err
		}
		content = fetched
	default:
		return nil, fmt.Errorf("either url or content is required")
	}

	parsed, err := ParseSKILLMD(content)
	if err != nil {
		return nil, fmt.Errorf("parse skill: %w", err)
	}

	return imp.upsert(ctx, parsed)
}

func (imp *Importer) fetchURL(ctx context.Context, url string) (string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}

	resp, err := imp.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("fetch url: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch url %q: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024)) // 512 KB limit
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	return string(body), nil
}

func (imp *Importer) upsert(ctx context.Context, parsed *ParsedSkill) (*ImportResult, error) {
	slug := parsed.Meta.Name // already slugified by ParseSKILLMD

	displayName := parsed.Meta.DisplayName
	if displayName == "" {
		displayName = slug
	}

	version := parsed.Meta.Version
	if version == "" {
		version = "1.0.0"
	}

	category := parsed.Meta.Category
	if category == "" {
		category = "CUSTOM"
	}

	credReqJSON := "[]"
	if len(parsed.Meta.CredentialRequirements) > 0 {
		b, _ := json.Marshal(parsed.Meta.CredentialRequirements)
		credReqJSON = string(b)
	}

	tagsJSON := "[]"
	if len(parsed.Meta.Tags) > 0 {
		b, _ := json.Marshal(parsed.Meta.Tags)
		tagsJSON = string(b)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Check if a skill with this slug already exists
	var existingID string
	err := imp.db.QueryRowContext(ctx, "SELECT id FROM skills WHERE slug = ?", slug).Scan(&existingID)

	if errors.Is(err, sql.ErrNoRows) {
		// INSERT new skill
		newID := generateSkillID()
		_, insertErr := imp.db.ExecContext(ctx, `
			INSERT INTO skills (
				id, name, slug, display_name, description, version, author,
				category, source, icon, credential_requirements, tags, content,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'CUSTOM', ?, ?, ?, ?, ?, ?)`,
			newID, displayName, slug, displayName,
			nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
			category, nullableStr(parsed.Meta.Icon),
			credReqJSON, tagsJSON, parsed.Content,
			now, now,
		)
		if insertErr != nil {
			if strings.Contains(insertErr.Error(), "UNIQUE constraint failed: skills.name") {
				return nil, fmt.Errorf("a skill with name %q already exists", displayName)
			}
			return nil, fmt.Errorf("insert skill: %w", insertErr)
		}
		return &ImportResult{
			SkillID: newID,
			Name:    displayName,
			Slug:    slug,
			Created: true,
		}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("check skill existence: %w", err)
	}

	// UPDATE existing skill (keep same ID to preserve agent_skills references)
	_, updateErr := imp.db.ExecContext(ctx, `
		UPDATE skills SET
			name = ?, display_name = ?, description = ?, version = ?, author = ?,
			category = ?, source = 'CUSTOM', icon = ?,
			credential_requirements = ?, tags = ?, content = ?, updated_at = ?
		WHERE id = ?`,
		displayName, displayName,
		nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
		category, nullableStr(parsed.Meta.Icon),
		credReqJSON, tagsJSON, parsed.Content,
		now, existingID,
	)
	if updateErr != nil {
		return nil, fmt.Errorf("update skill: %w", updateErr)
	}

	return &ImportResult{
		SkillID: existingID,
		Name:    displayName,
		Slug:    slug,
		Created: false,
	}, nil
}

// nullableStr returns nil for an empty string, enabling NULL storage in SQLite.
func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// generateSkillID produces a short random hex ID with a "sk_" prefix.
func generateSkillID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		// Fallback: use timestamp-based entropy to fill all 12 bytes
		ts := time.Now().UnixNano()
		b[0] = byte(ts >> 56)
		b[1] = byte(ts >> 48)
		b[2] = byte(ts >> 40)
		b[3] = byte(ts >> 32)
		b[4] = byte(ts >> 24)
		b[5] = byte(ts >> 16)
		b[6] = byte(ts >> 8)
		b[7] = byte(ts)
		ts2 := time.Now().UnixNano()
		b[8] = byte(ts2 >> 24)
		b[9] = byte(ts2 >> 16)
		b[10] = byte(ts2 >> 8)
		b[11] = byte(ts2)
	}
	return "sk_" + hex.EncodeToString(b)
}
