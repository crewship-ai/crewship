package skills

import (
	"encoding/json"
	"fmt"
)

// ImportFields are the skills-row values an import derives from a SKILL.md
// body. The importer writes exactly these (upsertEnriched), and `crewship
// apply` derives them from the local file to tell whether re-importing it
// would change anything (#5334). One function computes them for both sides,
// so the comparison cannot drift from what the server stores.
type ImportFields struct {
	Slug        string
	DisplayName string // also written to name
	Description string // "" is stored as NULL
	Version     string
	Author      string // "" is stored as NULL
	Category    string
	Icon        string // "" is stored as NULL
	// CredentialRequirements and Tags are stored as JSON arrays.
	CredentialRequirements string
	Tags                   string
	Content                string
	// Vendor, SPDXLicense and License are written with COALESCE on update:
	// an empty value keeps whatever the row holds.
	Vendor             string
	SPDXLicense        string
	License            string
	ScanStatus         string
	DescriptionQuality string // "" is stored as NULL
}

// deriveImportFields is the single place the importer turns a parsed skill
// into row values.
func deriveImportFields(parsed *ParsedSkill, vendor, spdx string, scan ScanResult) (ImportFields, error) {
	f := ImportFields{
		Slug:        parsed.Meta.Name,
		DisplayName: parsed.Meta.DisplayName,
		Description: parsed.Meta.Description,
		Version:     parsed.Meta.Version,
		Author:      parsed.Meta.Author,
		Category:    parsed.Meta.Category,
		Icon:        parsed.Meta.Icon,
		Content:     parsed.Content,
		Vendor:      vendor,
		SPDXLicense: spdx,
		License:     parsed.Meta.License,
		ScanStatus:  scan.Status,
		// Reuse description_quality for the scan reason: if a skill has both
		// a poor description AND an injection flag, the injection reason wins
		// because it's the safety-relevant one.
		DescriptionQuality:     parsed.DescriptionQuality,
		CredentialRequirements: "[]",
		Tags:                   "[]",
	}
	if f.DisplayName == "" {
		f.DisplayName = f.Slug
	}
	if f.Version == "" {
		f.Version = "1.0.0"
	}
	if f.Category == "" {
		f.Category = "CUSTOM"
	}
	if f.ScanStatus == "" {
		f.ScanStatus = "UNSCANNED"
	}
	if scan.Status == "FLAGGED" {
		f.DescriptionQuality = scan.Reason
	}
	if len(parsed.Meta.CredentialRequirements) > 0 {
		b, err := json.Marshal(parsed.Meta.CredentialRequirements)
		if err != nil {
			return ImportFields{}, fmt.Errorf("marshal credential_requirements: %w", err)
		}
		f.CredentialRequirements = string(b)
	}
	if len(parsed.Meta.Tags) > 0 {
		b, err := json.Marshal(parsed.Meta.Tags)
		if err != nil {
			return ImportFields{}, fmt.Errorf("marshal tags: %w", err)
		}
		f.Tags = string(b)
	}
	return f, nil
}

// ContentImportFields derives the row values a pasted-content import of
// content would write, through the same parse, licence detection and scan
// as Import.
func ContentImportFields(content string) (ImportFields, error) {
	parsed, err := ParseSKILLMD(content)
	if err != nil {
		return ImportFields{}, fmt.Errorf("parse skill: %w", err)
	}
	vendor := parsed.Meta.Vendor
	if vendor == "" {
		vendor = "community"
	}
	return deriveImportFields(parsed, vendor, DetectSPDX(parsed.Meta.License), ScanContent(parsed.Content))
}

// StoredSkill is the part of GET /api/v1/skills/{id} an import writes.
// Pointer fields are nullable columns. A server that does not return a
// field leaves it nil, which never matches a value the import would write.
type StoredSkill struct {
	Slug                   string  `json:"slug"`
	Name                   string  `json:"name"`
	DisplayName            string  `json:"display_name"`
	Description            *string `json:"description"`
	Version                string  `json:"version"`
	Author                 *string `json:"author"`
	Category               string  `json:"category"`
	Icon                   *string `json:"icon"`
	CredentialRequirements *string `json:"credential_requirements"`
	Tags                   *string `json:"tags"`
	Content                *string `json:"content"`
	Vendor                 *string `json:"vendor"`
	SPDXLicense            *string `json:"spdx_license"`
	License                *string `json:"license"`
	ScanStatus             string  `json:"scan_status"`
	DescriptionQuality     *string `json:"description_quality"`
}

// Matches reports whether importing these fields over the stored row would
// leave every imported column as it is. Anything it cannot confirm — a
// missing field, an older server — is a difference, so the caller keeps the
// write.
func (f ImportFields) Matches(s StoredSkill) bool {
	nullable := func(want string, got *string) bool {
		if want == "" {
			return got == nil
		}
		return got != nil && *got == want
	}
	// COALESCE(?, column): an empty value leaves the column alone.
	coalesced := func(want string, got *string) bool {
		return want == "" || (got != nil && *got == want)
	}
	return s.Slug == f.Slug &&
		s.Name == f.DisplayName &&
		s.DisplayName == f.DisplayName &&
		nullable(f.Description, s.Description) &&
		s.Version == f.Version &&
		nullable(f.Author, s.Author) &&
		s.Category == f.Category &&
		nullable(f.Icon, s.Icon) &&
		s.CredentialRequirements != nil && *s.CredentialRequirements == f.CredentialRequirements &&
		s.Tags != nil && *s.Tags == f.Tags &&
		s.Content != nil && *s.Content == f.Content &&
		coalesced(f.Vendor, s.Vendor) &&
		coalesced(f.SPDXLicense, s.SPDXLicense) &&
		coalesced(f.License, s.License) &&
		s.ScanStatus == f.ScanStatus &&
		nullable(f.DescriptionQuality, s.DescriptionQuality)
}
