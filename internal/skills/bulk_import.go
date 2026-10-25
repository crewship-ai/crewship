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
	// Truncated is set when the walk hit maxBulkSkills before
	// completing. Callers must surface this — silently treating a
	// truncated import as success means everything past the cap is
	// silently dropped without the operator knowing they need to split
	// the source repo or raise the limit.
	Truncated bool
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

		if req.GitRef != "" {
			if err := validateGitRef(req.GitRef); err != nil {
				return nil, err
			}
		}
		plan, err := planGitClone(ctx, req.GitURL, imp.gitPolicy, nil)
		if err != nil {
			return nil, err
		}
		dir, err := os.MkdirTemp("", "crewship-skills-import-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		home, err := os.MkdirTemp("", "crewship-skills-git-*")
		if err != nil {
			return nil, fmt.Errorf("create git isolation dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(home) }()
		template := filepath.Join(home, "empty-template")
		if err := os.Mkdir(template, 0700); err != nil {
			return nil, fmt.Errorf("create git template: %w", err)
		}
		gitPath, err := exec.LookPath("git")
		if err != nil {
			return nil, fmt.Errorf("bulk import via git_url requires git")
		}
		gitPath, err = filepath.Abs(gitPath)
		if err != nil {
			return nil, fmt.Errorf("bulk import via git_url requires absolute git path")
		}
		cloneCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if err := qualifyPinnedGit(cloneCtx, gitPath, home); err != nil {
			return nil, err
		}
		args, err := pinnedGitCloneArgs(plan, template, dir, req.GitRef)
		if err != nil {
			return nil, err
		}
		if output, err := runIsolatedGit(cloneCtx, gitPath, home, args...); err != nil {
			imp.logger.Error("git clone failed", "git_url", redactGitURL(req.GitURL), "error", err, "output", output)
			return nil, fmt.Errorf("git clone failed for %q (see server logs for detail)", redactGitURL(req.GitURL))
		}

		root = dir
		source = redactGitURL(req.GitURL)
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

	openedRoot, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open skills root: %w", err)
	}
	defer openedRoot.Close()
	walkErr := fs.WalkDir(openedRoot.FS(), ".", func(p string, d fs.DirEntry, err error) error {
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
			out.Truncated = true
			return fs.SkipAll
		}

		body, readErr := readSkillWithinRoot(openedRoot, p)
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

		res, importErr := imp.upsertEnriched(ctx, parsed, vendor, spdx, scan, source, "")
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

// validateGitRef enforces a conservative subset of git-check-ref-format(1)
// before the ref name reaches `git clone --branch`. Without this a
// crafted ref like "--upload-pack=evil" or one containing a NUL would
// either be interpreted by git as another option flag or terminate the
// argv list early on some shells. Refs are user-supplied via the bulk
// import request and untrusted by definition; the subset below covers
// every legitimate branch/tag/sha we have ever seen in the wild.
func validateGitRef(ref string) error {
	if ref == "" {
		return fmt.Errorf("git ref must not be empty")
	}
	if len(ref) > 255 {
		return fmt.Errorf("git ref too long")
	}
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("git ref must not start with '-': %q", ref)
	}
	for _, r := range ref {
		// allowed: letters, digits, '.', '_', '/', '-' (only after the
		// first char). Anything else — control chars, NUL, spaces, '$',
		// '`', backticks — is rejected.
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '/' || r == '-':
		default:
			return fmt.Errorf("git ref contains disallowed char %q in %q", r, ref)
		}
	}
	if strings.Contains(ref, "..") {
		return fmt.Errorf("git ref must not contain '..': %q", ref)
	}
	return nil
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
// This is a syntax-level helper; production BulkImport additionally resolves,
// validates and pins the connection through planGitClone and qualified Git.
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
	// Block userinfo. https://token@host/repo.git is a common shape
	// for personal-access-token-authenticated clones, but accepting it
	// here would (a) tempt operators to paste tokens through the API,
	// (b) force us to redact in every error/log path, and (c) leak via
	// every place we echo the URL back in responses or stack traces.
	// If a private repo really needs auth, the right answer is a
	// per-server credential helper (.gitconfig insteadOf), not a token
	// in the URL.
	if u.User != nil {
		return fmt.Errorf("git URL must not embed credentials; got userinfo in %q", host)
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

// redactGitURL strips any userinfo segment from a URL so credentials
// that somehow slipped through validateGitURL (or were added by a
// future caller) cannot land in error messages or response bodies.
// Best-effort: if parsing fails we fall back to the literal "<redacted>"
// rather than the raw input — better to lose the URL in a log line
// than to echo a token by accident.
func redactGitURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "<redacted>"
	}
	u.User = nil
	return u.String()
}

func pathMatchesAny(p string, globs []string) bool {
	for _, g := range globs {
		if ok, _ := filepath.Match(g, p); ok {
			return true
		}
	}
	return false
}
