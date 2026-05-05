package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Bulk-import safety envelopes. maxSkillFileBytes mirrors the
// URL-fetch limit so a single SKILL.md cannot be larger than what
// the URL path would have accepted; maxBulkSkills caps the matched
// set so a runaway walk doesn't pin memory regardless of clone
// depth.
const (
	maxSkillFileBytes = int64(512 * 1024)
	maxBulkSkills     = 500
)

// BulkImportRequest specifies a directory or git repository to walk for
// SKILL.md files. Exactly one of GitURL or LocalPath must be set.
//
// AllowUnsafeLicense bypasses the SPDX allowlist gate for the entire
// batch — used by the CLI's --unsafe-license flag. The flag is per-call,
// not persistent on the row, so a future re-import will re-evaluate.
//
// Vendor defaults to "community" for git imports (the org/repo pair is
// recorded in the homepage column for traceability) and "local" for
// path imports. The CLI can override this if the upstream repo has a
// canonical vendor name (e.g. "anthropic" for the official skills repo).
type BulkImportRequest struct {
	GitURL             string
	GitRef             string // branch/tag/sha; defaults to repo's default branch
	LocalPath          string
	Paths              []string // optional glob filters relative to root
	Vendor             string   // override; empty falls back to source-derived default
	AllowUnsafeLicense bool
	DryRun             bool
}

// BulkImportResult summarises what one BulkImport call produced.
type BulkImportResult struct {
	Source        string
	Skills        []ImportResult
	Skipped       []SkippedSkill
	TotalFound    int
	TotalImported int
}

// SkippedSkill records a SKILL.md that was found but excluded — license
// rejection, parse failure, or quality gate. The CLI surfaces these to
// the user so they can investigate.
type SkippedSkill struct {
	Path   string
	Slug   string
	Reason string
}

// BulkImport walks a git repo or local directory tree for SKILL.md
// files and imports each one through the existing single-skill upsert
// path. License gate runs per-skill so a mixed-license repo's allowed
// skills land while disallowed ones are reported in Skipped.
//
// Git clones use the host's `git` binary with --depth 1 --filter=blob:none
// for a minimal-bandwidth checkout. Cloning to an OS temp dir; cleaned
// up on return regardless of success. Pre-flight: errors out clearly if
// `git` is not in PATH so callers know to install it (vs the more
// confusing "exec: git: not found" deep in a stack trace).
func (imp *Importer) BulkImport(ctx context.Context, req BulkImportRequest) (*BulkImportResult, error) {
	switch {
	case req.GitURL == "" && req.LocalPath == "":
		return nil, fmt.Errorf("bulk import requires git_url or local_path")
	case req.GitURL != "" && req.LocalPath != "":
		return nil, fmt.Errorf("bulk import accepts git_url or local_path, not both")
	}

	root := req.LocalPath
	source := root

	if req.GitURL != "" {
		if _, err := exec.LookPath("git"); err != nil {
			return nil, fmt.Errorf("bulk import via git_url requires `git` in PATH: %w", err)
		}
		if err := validateGitURL(req.GitURL); err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp("", "crewship-skills-import-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()

		args := []string{
			"clone", "--depth", "1",
			"--filter=blob:none",
			"--single-branch",
		}
		if req.GitRef != "" {
			args = append(args, "--branch", req.GitRef)
		}
		args = append(args, req.GitURL, dir)
		cloneCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cloneCtx, "git", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git clone %q: %w (output: %s)", req.GitURL, err, strings.TrimSpace(string(out)))
		}
		root = dir
		source = req.GitURL
	}

	defaultVendor := req.Vendor
	if defaultVendor == "" {
		if req.GitURL != "" {
			defaultVendor = "community"
		} else {
			defaultVendor = "local"
		}
	}

	out := &BulkImportResult{Source: source}

	walkErr := fs.WalkDir(os.DirFS(root), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip entries we can't stat — non-fatal
		}
		if d.IsDir() {
			// Skip the obvious non-skill directories early so we don't
			// waste time descending into node_modules etc. Keeping this
			// list short — anything more aggressive risks missing real
			// skills nested under a docs/ or examples/ folder.
			base := d.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Base(p) != "SKILL.md" {
			return nil
		}
		if len(req.Paths) > 0 && !pathMatchesAny(p, req.Paths) {
			return nil
		}
		out.TotalFound++

		// Hard cap on TotalFound so a malicious or accidentally huge
		// repo (10k+ SKILL.md files) can't run the importer's response
		// memory or wall time unbounded. 500 is well past anything
		// real-world (anthropics/skills has 18); a repo above this is
		// either pathological or being used as a DoS vector.
		if out.TotalFound > maxBulkSkills {
			return fs.SkipAll
		}

		fullPath := filepath.Join(root, p)
		// Per-file size cap: SKILL.md is meant to be a few hundred lines
		// of markdown. A 10MB+ "SKILL.md" is either a binary-blob attack
		// or a packaging mistake; reading it would balloon the importer's
		// memory and the eventual content column. Keep this aligned with
		// the URL-fetch limit (512 KB) so single and bulk paths share the
		// same envelope.
		stat, statErr := os.Stat(fullPath)
		if statErr != nil {
			out.Skipped = append(out.Skipped, SkippedSkill{Path: p, Reason: "stat: " + statErr.Error()})
			return nil
		}
		if stat.Size() > maxSkillFileBytes {
			out.Skipped = append(out.Skipped, SkippedSkill{
				Path:   p,
				Reason: fmt.Sprintf("file %d bytes exceeds %d-byte SKILL.md limit", stat.Size(), maxSkillFileBytes),
			})
			return nil
		}
		body, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			out.Skipped = append(out.Skipped, SkippedSkill{Path: p, Reason: "read: " + readErr.Error()})
			return nil
		}

		parsed, parseErr := ParseSKILLMD(string(body))
		if parseErr != nil {
			out.Skipped = append(out.Skipped, SkippedSkill{Path: p, Reason: "parse: " + parseErr.Error()})
			return nil
		}

		spdx := DetectSPDX(parsed.Meta.License)
		if !req.AllowUnsafeLicense && !LicenseAllowed(spdx) {
			out.Skipped = append(out.Skipped, SkippedSkill{
				Path: p, Slug: parsed.Meta.Name,
				Reason: (&LicenseError{Detected: spdx, Raw: parsed.Meta.License}).Error(),
			})
			return nil
		}

		if req.DryRun {
			out.Skipped = append(out.Skipped, SkippedSkill{Path: p, Slug: parsed.Meta.Name, Reason: "dry-run"})
			return nil
		}

		scan := ScanContent(parsed.Content)
		vendor := parsed.Meta.Vendor
		if vendor == "" {
			vendor = defaultVendor
		}

		res, importErr := imp.upsertEnriched(ctx, parsed, vendor, spdx, scan, source)
		if importErr != nil {
			out.Skipped = append(out.Skipped, SkippedSkill{
				Path: p, Slug: parsed.Meta.Name,
				Reason: "upsert: " + importErr.Error(),
			})
			return nil
		}
		out.Skills = append(out.Skills, *res)
		out.TotalImported++
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
		return out, fmt.Errorf("walk skills: %w", walkErr)
	}
	return out, nil
}

// validateGitURL rejects file:// and git@ shorthand URLs that would
// allow arbitrary local-path or SSH-key-protected access from a
// network-exposed importer endpoint. HTTPS to public registries is the
// only intended surface.
//
// Also mirrors ValidateImportURL's SSRF guard: a literal IP host that
// resolves to localhost / private / link-local space is rejected so a
// malicious operator can't use the bulk-import endpoint to clone from
// 169.254.169.254 (cloud metadata) or 10.0.0.0/8 (internal git servers).
// Hostnames are not pre-resolved here — git itself does the DNS lookup
// at clone time; a DNS rebinding attack against git is theoretically
// possible but git's HTTP transport doesn't follow Host-overriding
// redirects, so the practical exposure mirrors ValidateImportURL's
// known limitation.
func validateGitURL(raw string) error {
	if strings.HasPrefix(raw, "file://") || strings.HasPrefix(raw, "git@") {
		return fmt.Errorf("bulk import only supports https git URLs; got %q", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse git URL: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("bulk import requires https git URLs; got scheme %q", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("git URL missing host: %q", raw)
	}
	if strings.EqualFold(host, "localhost") {
		return fmt.Errorf("localhost git URLs are not allowed")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("private/internal IP addresses are not allowed in git URL")
		}
	}
	return nil
}

func pathMatchesAny(p string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, p); ok {
			return true
		}
	}
	return false
}
