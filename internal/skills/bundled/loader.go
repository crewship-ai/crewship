// Package bundled embeds the official skills shipped with Crewship and
// installs them on first startup.
//
// We bundle a curated subset of github.com/anthropics/skills (Apache-2.0)
// so a fresh install has useful skills without internet access — the
// "self-hosted runtime" positioning recorded in project memory makes
// offline-first the default. Third-party skills land via the import flow
// (\`crewship skill import\`) and are not embedded here.
//
// Bundled content layout:
//
//	bundled/
//	  anthropic/<slug>/SKILL.md
//	  _licenses/anthropic-Apache-2.0.txt
//
// The 4 source-available skills (docx, pdf, pptx, xlsx) from the upstream
// repo are deliberately excluded — they ship under a non-OSS license and
// fail the SPDX allowlist gate. Users wanting them must opt in explicitly
// via the import flow once the gate accepts the license.
package bundled

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/skills"
)

//go:embed all:anthropic _licenses
var bundledFS embed.FS

// FS exposes the embedded bundled-skill filesystem for tests / inspection.
// Production callers use [Install] instead.
func FS() fs.FS { return bundledFS }

// vendorMeta captures per-vendor defaults applied to every skill in that
// folder. The upstream Anthropic SKILL.md frontmatter is intentionally
// minimal (name + description only) and doesn't carry our taxonomy
// fields, so we attach them here at install time.
type vendorMeta struct {
	displayLicense string // freeform attribution string in the `license` column
	spdxLicense    string // canonical SPDX id used by the gate
	maturity       string // SkillMaturity enum value
	homepageRoot   string // upstream URL prefix; per-skill segment is appended
}

var vendors = map[string]vendorMeta{
	"anthropic": {
		displayLicense: "Apache-2.0 (anthropics/skills)",
		spdxLicense:    "Apache-2.0",
		maturity:       "OFFICIAL",
		homepageRoot:   "https://github.com/anthropics/skills/tree/main/skills/",
	},
}

// skillManifest enriches a single bundled skill with metadata that is not
// in upstream frontmatter — the category each skill belongs to in our
// 12-domain taxonomy, and any per-skill overrides. Keys are
// "<vendor>/<slug>".
type skillManifest struct {
	category string
	icon     string
}

var manifests = map[string]skillManifest{
	"anthropic/skill-creator":         {category: "CODING", icon: "sparkles"},
	"anthropic/mcp-builder":           {category: "CODING", icon: "plug"},
	"anthropic/claude-api":            {category: "CODING", icon: "code-2"},
	"anthropic/frontend-design":       {category: "DESIGN", icon: "palette"},
	"anthropic/web-artifacts-builder": {category: "CODING", icon: "blocks"},
	"anthropic/webapp-testing":        {category: "CODING", icon: "flask-conical"},
	"anthropic/doc-coauthoring":       {category: "WRITING", icon: "file-text"},
	"anthropic/internal-comms":        {category: "WRITING", icon: "megaphone"},
	"anthropic/brand-guidelines":      {category: "DESIGN", icon: "swatch-book"},
	"anthropic/canvas-design":         {category: "DESIGN", icon: "frame"},
	"anthropic/theme-factory":         {category: "DESIGN", icon: "paintbrush"},
	"anthropic/algorithmic-art":       {category: "DESIGN", icon: "shapes"},
	"anthropic/slack-gif-creator":     {category: "DESIGN", icon: "image"},
}

// Install upserts every embedded SKILL.md into the skills table. Idempotent:
// re-running on an unchanged build is a no-op aside from updated_at.
//
// Errors from a single skill are logged but do not abort the loop — losing
// one bundled skill should not block startup.
func Install(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	now := time.Now().UTC().Format(time.RFC3339)
	installed := 0
	skipped := 0
	for vendor, vmeta := range vendors {
		root := vendor
		entries, err := fs.ReadDir(bundledFS, root)
		if err != nil {
			logger.Warn("bundled vendor missing", "vendor", vendor, "error", err)
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			slug := entry.Name()
			skillPath := path.Join(root, slug, "SKILL.md")
			body, err := bundledFS.ReadFile(skillPath)
			if err != nil {
				logger.Warn("read bundled skill", "path", skillPath, "error", err)
				continue
			}
			parsed, err := skills.ParseSKILLMD(string(body))
			if err != nil {
				logger.Warn("parse bundled skill", "path", skillPath, "error", err)
				continue
			}
			key := vendor + "/" + slug
			man := manifests[key]
			if man.category == "" {
				man.category = "CUSTOM"
			}
			if err := upsert(ctx, db, parsed, vendor, slug, vmeta, man, now); err != nil {
				logger.Warn("upsert bundled skill", "key", key, "error", err)
				continue
			}
			installed++
		}
	}
	logger.Info("bundled skills installed", "count", installed, "skipped", skipped)
	return nil
}

// upsert writes (or refreshes) a single bundled skill. The lookup key is
// the (vendor, slug) pair, not the bare slug — bundled skills are owned
// by their vendor and re-running install must not collide with a
// user-imported skill of the same slug under "community/".
func upsert(
	ctx context.Context,
	db *sql.DB,
	parsed *skills.ParsedSkill,
	vendor, slug string,
	vmeta vendorMeta,
	man skillManifest,
	now string,
) error {
	displayName := parsed.Meta.DisplayName
	if displayName == "" {
		displayName = humaniseSlug(slug)
	}
	version := parsed.Meta.Version
	if version == "" {
		version = "1.0.0"
	}
	homepage := vmeta.homepageRoot + slug

	tagsJSON := "[]"
	if len(parsed.Meta.Tags) > 0 {
		tagsJSON = tagsToJSON(parsed.Meta.Tags)
	}

	descQuality := nullableStr(parsed.DescriptionQuality)

	var existingID string
	err := db.QueryRowContext(ctx,
		`SELECT id FROM skills WHERE vendor = ? AND slug = ?`,
		vendor, slug,
	).Scan(&existingID)

	if errors.Is(err, sql.ErrNoRows) {
		newID := generateBundledID(vendor, slug)
		_, insertErr := db.ExecContext(ctx, `
			INSERT INTO skills (
				id, name, slug, display_name, description, version, author,
				license, category, source, icon, content, tags,
				vendor, homepage, spdx_license, runtime, maturity, scan_status,
				description_quality, verification, pricing_tier,
				created_at, updated_at
			) VALUES (
				?, ?, ?, ?, ?, ?, ?,
				?, ?, 'BUNDLED', ?, ?, ?,
				?, ?, ?, 'INSTRUCTIONS', ?, 'CLEAN',
				?, 'VERIFIED', 'FREE',
				?, ?
			)`,
			newID, slug, slug, displayName,
			nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
			vmeta.displayLicense, man.category, man.icon, parsed.Content, tagsJSON,
			vendor, homepage, vmeta.spdxLicense, vmeta.maturity,
			descQuality,
			now, now,
		)
		if insertErr != nil {
			return fmt.Errorf("insert: %w", insertErr)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup existing: %w", err)
	}

	_, updateErr := db.ExecContext(ctx, `
		UPDATE skills SET
			display_name = ?,
			description = ?,
			version = ?,
			author = ?,
			license = ?,
			category = ?,
			icon = ?,
			content = ?,
			tags = ?,
			homepage = ?,
			spdx_license = ?,
			maturity = ?,
			description_quality = ?,
			updated_at = ?
		WHERE id = ?`,
		displayName,
		nullableStr(parsed.Meta.Description), version, nullableStr(parsed.Meta.Author),
		vmeta.displayLicense, man.category, man.icon, parsed.Content, tagsJSON,
		homepage, vmeta.spdxLicense, vmeta.maturity,
		descQuality,
		now, existingID,
	)
	if updateErr != nil {
		return fmt.Errorf("update: %w", updateErr)
	}
	return nil
}

// generateBundledID produces a deterministic-looking but
// crypto-random-prefixed ID for a bundled skill. Bundled skills could use
// a stable ID derived from (vendor, slug) so re-installs across machines
// agree, but that would clash with the existing "sk_" + random hex
// convention used by the importer. Keep the convention; the (vendor, slug)
// uniqueness lives at the SQL layer instead.
func generateBundledID(vendor, slug string) string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic("bundled: crypto/rand unavailable: " + err.Error())
	}
	_ = vendor
	_ = slug
	return "sk_" + hex.EncodeToString(b)
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func humaniseSlug(s string) string {
	parts := strings.Split(s, "-")
	for i, p := range parts {
		if p == "" {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, " ")
}

// tagsToJSON renders a slice of strings as a compact JSON array. We keep
// this hand-rolled rather than import encoding/json — tag bodies are
// already URL-safe slugs by convention, and the serialiser only needs to
// quote-escape on the unhappy path.
func tagsToJSON(tags []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, t := range tags {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		for _, r := range t {
			switch r {
			case '"', '\\':
				b.WriteByte('\\')
				b.WriteRune(r)
			default:
				b.WriteRune(r)
			}
		}
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}
