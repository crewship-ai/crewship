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
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// ImportRequest specifies a skill to import — either from a URL or pasted content.
// Exactly one of URL or Content must be non-empty. AllowUnsafeLicense bypasses
// the SPDX allowlist gate (same semantics as BulkImport's flag).
type ImportRequest struct {
	URL                string
	Content            string
	AllowUnsafeLicense bool
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
	// SkipURLValidation disables SSRF checks (testing only).
	SkipURLValidation bool
}

// NewImporter creates an Importer with a 30-second HTTP timeout.
// The HTTP client validates redirect targets against SSRF checks.
func NewImporter(db *sql.DB, logger *slog.Logger) *Importer {
	imp := &Importer{
		db:     db,
		logger: logger,
	}
	imp.client = &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if imp.SkipURLValidation {
				return nil
			}
			return ValidateImportURL(req.Context(), req.URL.String())
		},
	}
	return imp
}

// Import imports a skill from the given request and upserts it into the database.
// The workspaceID and userID parameters are accepted for API handler compatibility
// but currently unused — skills are global platform-wide resources (no workspace_id
// column in the skills table). They may be used for audit logging in the future.
//
// Both the single-skill paths (URL and pasted content) run through the
// same prompt-injection scanner + SPDX detection that BulkImport uses.
// Earlier this branch had a "lite" upsert that skipped both gates; that
// regression let single-file imports land with scan_status=UNSCANNED
// even on bodies the bulk path would have flagged. The single-skill API
// keeps the SPDX gate optional via AllowUnsafeLicense — the UI surfaces
// the same checkbox as the bulk-repo tab.
func (imp *Importer) Import(ctx context.Context, _, _ string, req ImportRequest) (*ImportResult, error) {
	var content string
	source := ""

	switch {
	case req.URL != "" && req.Content != "":
		return nil, fmt.Errorf("provide either url or content, not both")
	case req.Content != "":
		content = req.Content
	case req.URL != "":
		if !imp.SkipURLValidation {
			if err := ValidateImportURL(ctx, req.URL); err != nil {
				return nil, fmt.Errorf("validate import URL: %w", err)
			}
		}
		normalised, err := NormalizeSkillURL(req.URL)
		if err != nil {
			return nil, fmt.Errorf("normalize skill URL: %w", err)
		}
		fetched, err := imp.fetchURL(ctx, normalised)
		if err != nil {
			return nil, err
		}
		content = fetched
		source = normalised
	default:
		return nil, fmt.Errorf("either url or content is required")
	}

	parsed, err := ParseSKILLMD(content)
	if err != nil {
		return nil, fmt.Errorf("parse skill: %w", err)
	}

	spdx := DetectSPDX(parsed.Meta.License)
	if !req.AllowUnsafeLicense && parsed.Meta.License != "" && !LicenseAllowed(spdx) {
		return nil, &LicenseError{Detected: spdx, Raw: parsed.Meta.License}
	}

	scan := ScanContent(parsed.Content)
	vendor := parsed.Meta.Vendor
	if vendor == "" {
		vendor = "community"
	}

	return imp.upsertEnriched(ctx, parsed, vendor, spdx, scan, source)
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

	const limit = int64(512 * 1024)
	lr := &io.LimitedReader{R: resp.Body, N: limit + 1}
	body, err := io.ReadAll(lr)
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	if int64(len(body)) > limit {
		return "", fmt.Errorf("response body exceeds 512 KB limit")
	}
	return string(body), nil
}

// upsertEnriched is the v65-aware writer used by both single-skill and
// bulk-repo import paths. It populates vendor / spdx_license /
// scan_status / description_quality alongside the core columns. The
// pre-v65 "lite" upsert that wrote those as NULL was removed when the
// scanner gate was wired into the single path — every import now lands
// with a real scan result.
//
// Identity is still by slug — composite (vendor, slug) lookup is
// future work once we have multi-source imports producing collisions.
// For now bulk imports under different vendors are upserted by slug,
// which means the LAST import wins on a conflict. The CLI surfaces
// this via the "updated" flag in ImportResult so users can spot
// shadowed skills.
func (imp *Importer) upsertEnriched(
	ctx context.Context,
	parsed *ParsedSkill,
	vendor string,
	spdx string,
	scan ScanResult,
	homepage string,
) (*ImportResult, error) {
	slug := parsed.Meta.Name
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
		b, err := json.Marshal(parsed.Meta.CredentialRequirements)
		if err != nil {
			return nil, fmt.Errorf("marshal credential_requirements: %w", err)
		}
		credReqJSON = string(b)
	}

	tagsJSON := "[]"
	if len(parsed.Meta.Tags) > 0 {
		b, err := json.Marshal(parsed.Meta.Tags)
		if err != nil {
			return nil, fmt.Errorf("marshal tags: %w", err)
		}
		tagsJSON = string(b)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	scanStatus := scan.Status
	if scanStatus == "" {
		scanStatus = "UNSCANNED"
	}

	descQuality := parsed.DescriptionQuality
	if scan.Status == "FLAGGED" {
		// Reuse description_quality column for the scan reason — keeps
		// the schema slim and the UI surface single. If a skill has
		// both a poor description AND an injection flag, the injection
		// reason wins because it's the safety-relevant one.
		descQuality = scan.Reason
	}

	var existingID, existingSource string
	err := imp.db.QueryRowContext(ctx,
		"SELECT id, source FROM skills WHERE slug = ?", slug).Scan(&existingID, &existingSource)

	// BUNDLED skills are the curated set we ship in the binary
	// (anthropic/* under Apache-2.0). Letting an arbitrary import
	// silently overwrite their content is a supply-chain footgun:
	// the slug stays "official" in the UI but the body is whatever
	// a user pasted. Block the upsert and tell the caller the
	// concrete remediation — pick a different slug, or `crewship
	// skill delete` first if they really meant to replace it.
	if err == nil && existingSource == "BUNDLED" {
		return nil, fmt.Errorf(
			"skill %q is BUNDLED (curated, ships with the binary); "+
				"refusing to overwrite. Pick a different slug, or delete the bundled "+
				"row first if this is intentional.", slug)
	}

	if errors.Is(err, sql.ErrNoRows) {
		newID := generateSkillID()
		_, insertErr := imp.db.ExecContext(ctx, `
			INSERT INTO skills (
				id, name, slug, display_name, description, version, author,
				category, source, icon, credential_requirements, tags, content,
				vendor, homepage, spdx_license, runtime, maturity, scan_status,
				description_quality, license,
				created_at, updated_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?,
				?, 'CUSTOM', ?, ?, ?, ?,
				?, ?, ?, 'INSTRUCTIONS', 'COMMUNITY', ?,
				?, ?,
				?, ?
			)`,
			// name MUST match displayName for both INSERT and UPDATE
			// branches — the bare upsert at line 182 uses displayName,
			// and the GENERATED-source insert in skills_generate.go
			// also wants displayName. Setting name=slug here (as the
			// pre-fix code did) caused the column to silently diverge
			// between create and edit flows.
			newID, displayName, slug, displayName,
			nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
			category, nullableStr(parsed.Meta.Icon),
			credReqJSON, tagsJSON, parsed.Content,
			nullableStr(vendor), nullableStr(homepage), nullableStr(spdx), scanStatus,
			nullableStr(descQuality), nullableStr(parsed.Meta.License),
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

	_, updateErr := imp.db.ExecContext(ctx, `
		UPDATE skills SET
			name = ?, display_name = ?, description = ?, version = ?, author = ?,
			category = ?, icon = ?,
			credential_requirements = ?, tags = ?, content = ?,
			vendor = COALESCE(?, vendor),
			homepage = COALESCE(?, homepage),
			spdx_license = COALESCE(?, spdx_license),
			scan_status = ?,
			description_quality = ?,
			license = COALESCE(?, license),
			updated_at = ?
		WHERE id = ?`,
		displayName, displayName,
		nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
		category, nullableStr(parsed.Meta.Icon),
		credReqJSON, tagsJSON, parsed.Content,
		nullableStr(vendor), nullableStr(homepage), nullableStr(spdx), scanStatus,
		nullableStr(descQuality), nullableStr(parsed.Meta.License),
		now, existingID,
	)
	if updateErr != nil {
		if strings.Contains(updateErr.Error(), "UNIQUE constraint failed: skills.name") {
			return nil, fmt.Errorf("a skill with name %q already exists", displayName)
		}
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
// crypto/rand failure is exceptional on any production system (means
// /dev/urandom isn't available or the entropy pool is broken). The
// previous fallback wrote two nearly-identical UnixNano timestamps into
// the bytes, producing highly predictable IDs and easy collisions on
// rapid imports — strictly worse than failing loudly. Panic instead so
// callers learn about the underlying problem rather than later
// debugging duplicate "sk_" rows.
func generateSkillID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("skills: crypto/rand unavailable: " + err.Error())
	}
	return "sk_" + hex.EncodeToString(b)
}
