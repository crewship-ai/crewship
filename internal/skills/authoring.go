package skills

import (
	"fmt"
	"os"
	"path/filepath"
)

// StagedSkill is the outcome of staging an agent-authored SKILL.md document
// for human review.
type StagedSkill struct {
	Path string     // absolute path of the staged skill-<slug>.md file
	Slug string     // canonical slug derived from the frontmatter name
	Scan ScanResult // heuristic injection-scan verdict (CLEAN | FLAGGED)
}

// StageAuthoredSkill validates an agent-authored SKILL.md document and writes
// it into proposedDir for human review. It deliberately does NOT add the skill
// to the live registry: an operator promotes a staged skill through the
// proposed-approve flow, which re-runs the canonical importer (license + scan
// gates included). Staging into the same .proposed directory the consolidator
// uses means agent-authored skills surface in the existing review UI/CLI for
// free.
//
// Validation mirrors the import path — the document must carry YAML
// frontmatter with a usable name, and its body is run through the heuristic
// injection scanner. A FLAGGED verdict does not block staging (staging is
// itself a human gate); the verdict is returned so the caller can surface it
// and the reviewer sees it before approving.
func StageAuthoredSkill(proposedDir, content string) (StagedSkill, error) {
	parsed, err := ParseSKILLMD(content)
	if err != nil {
		return StagedSkill{}, fmt.Errorf("stage authored skill: invalid SKILL.md: %w", err)
	}
	slug := Slugify(parsed.Meta.Name)
	if slug == "" {
		return StagedSkill{}, fmt.Errorf("stage authored skill: frontmatter name is required and must slugify to a non-empty value")
	}

	scan := ScanContent(content)

	if err := os.MkdirAll(proposedDir, 0o755); err != nil {
		return StagedSkill{}, fmt.Errorf("stage authored skill: mkdir proposed dir: %w", err)
	}
	path, err := WriteUniqueSkillFile(proposedDir, slug, []byte(content))
	if err != nil {
		return StagedSkill{}, err
	}
	return StagedSkill{Path: path, Slug: slug, Scan: scan}, nil
}

// WriteUniqueSkillFile writes body to the first non-colliding
// skill-<slug>.md path under dir, using O_CREATE|O_EXCL so concurrent callers
// that pick the same slug serialise at the kernel level instead of clobbering
// each other's staged file. Returns the absolute path written.
func WriteUniqueSkillFile(dir, slug string, body []byte) (string, error) {
	for i := 1; i < 100; i++ {
		name := "skill-" + slug + ".md"
		if i > 1 {
			name = fmt.Sprintf("skill-%s-%d.md", slug, i)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				continue // suffix taken — try the next one
			}
			return "", fmt.Errorf("write skill file: open: %w", err)
		}
		if _, werr := f.Write(body); werr != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("write skill file: write body: %w", werr)
		}
		if cerr := f.Close(); cerr != nil {
			return "", fmt.Errorf("write skill file: close: %w", cerr)
		}
		return path, nil
	}
	return "", fmt.Errorf("write skill file: ran out of suffixes disambiguating skill-%s.md", slug)
}
