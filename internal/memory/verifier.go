package memory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// VerifierMode selects how aggressively a memory write is fact-checked
// before it lands on disk. Same opt-in tier model the scrubber uses —
// callers explicitly choose the mode; zero-value means "off".
type VerifierMode int

const (
	// VerifierOff skips every check. Default for backwards compat
	// with pre-PR-#3 call sites.
	VerifierOff VerifierMode = iota
	// VerifierCheap runs deterministic regex/Stat-only checks:
	// citation-staleness (file:line refs that point at missing
	// files or out-of-range lines). Cheap = no LLM, no DB query
	// beyond what's already in WriteConfig.
	VerifierCheap
	// VerifierLLM is reserved for the optional LLM verifier mode
	// described in PRD §7.3 — runs the cheap checks AND a small
	// LLM call asking "does this contradict prior stable memory?"
	// Implementation is plumbing-only in this commit; calling
	// with VerifierLLM today degrades to VerifierCheap with a
	// warn log.
	VerifierLLM
)

// VerifierConfig parameterises the verifier. CitationSearchRoots is
// the list of base directories the file:line citation check will
// stat under. The first root that contains the cited filename wins.
// Empty list disables the citation check (no errors, just no
// validation). Operators with bind-mounted source repos pass the
// repo roots here.
type VerifierConfig struct {
	Mode                VerifierMode
	CitationSearchRoots []string
}

// VerifierDecision is the verifier's binary verdict. Allow means the
// write proceeds; Reject means WriteFile returns the structured 422-
// shaped error envelope to its caller.
type VerifierDecision int

const (
	VerifierAllow VerifierDecision = iota
	VerifierReject
)

// VerifierResult is what VerifyWrite returns. Findings is the per-
// check breakdown — populated even on Allow so callers can log
// near-misses. Kind is the dominant rejection class when Decision is
// Reject; empty on Allow.
type VerifierResult struct {
	Decision VerifierDecision
	Kind     string // "stale_citation" | "contradiction" | ""
	Findings []VerifierFinding
}

// VerifierFinding is a single rule fire. CheckName names the rule;
// Detail carries the structured metadata the journal entry will
// echo back to operators (file path, line number, missing-file
// reason, etc).
type VerifierFinding struct {
	CheckName string
	Detail    map[string]any
}

// citationRE matches `path/to/file.ext:LINE` references. Path may
// contain forward slashes and dots; line is 1+ digits. Conservative
// on path chars so we don't false-positive on prose like "the v1.2
// release: line break". The capture groups are (path, line).
var citationRE = regexp.MustCompile(`([A-Za-z0-9_./\-]+\.[A-Za-z0-9]+):(\d+)`)

// VerifyWrite inspects `content` against the configured checks and
// returns a decision. The function is pure-fs + pure-regex in
// VerifierCheap mode — no DB, no network, no LLM. It can be called
// from any layer that has the content + a verifier config.
//
// Mode-specific behaviour:
//
//	VerifierOff    → returns Allow immediately
//	VerifierCheap  → runs citation-staleness only
//	VerifierLLM    → runs cheap checks; LLM half is unimplemented
//	                 (returns Allow with a Finding tagged "llm_skipped")
//
// Citation-staleness check:
//
//   - Extracts every `<path>.<ext>:<line>` reference via regex.
//   - For each, tries to resolve <path> under any CitationSearchRoots
//     entry. First root with a stat-able file wins.
//   - If no root resolves OR the line number exceeds the file's line
//     count, that citation is recorded as a Finding and the decision
//     becomes Reject (kind=stale_citation).
//   - With no CitationSearchRoots configured the check is a no-op
//     even on cited content — caller has nothing to validate against.
//
// The function is best-effort about ambiguity: a citation that
// COULD resolve under several roots stops at the first match. Two
// roots with the same file path resolves to the first one declared.
func VerifyWrite(ctx context.Context, content []byte, cfg VerifierConfig) (VerifierResult, error) {
	if cfg.Mode == VerifierOff {
		return VerifierResult{Decision: VerifierAllow}, nil
	}
	if err := ctx.Err(); err != nil {
		return VerifierResult{}, err
	}

	result := VerifierResult{Decision: VerifierAllow}

	// VerifierLLM mode placeholder: log a Finding so audit reviewers
	// know the optional LLM check was requested but not yet wired.
	// The cheap checks below still run.
	if cfg.Mode == VerifierLLM {
		result.Findings = append(result.Findings, VerifierFinding{
			CheckName: "llm_skipped",
			Detail:    map[string]any{"reason": "VerifierLLM mode not yet implemented; ran VerifierCheap"},
		})
	}

	// Citation-staleness check. Skipped when no search roots are
	// configured — operators may legitimately want the verifier on
	// without citation validation (cheap dedup, future contradiction).
	if len(cfg.CitationSearchRoots) > 0 {
		staleFindings := checkStaleCitations(string(content), cfg.CitationSearchRoots)
		if len(staleFindings) > 0 {
			result.Findings = append(result.Findings, staleFindings...)
			result.Decision = VerifierReject
			result.Kind = "stale_citation"
		}
	}

	return result, nil
}

// checkStaleCitations runs the regex pass + per-citation Stat check.
// Returns a Finding per stale citation; nil when every citation
// resolves cleanly. Stat errors that aren't IsNotExist (permission
// denied, dangling symlink) ALSO count as stale — the verifier's
// job is to ensure the cited file is readable and addressable;
// "exists but I can't read it" is operationally the same as missing.
func checkStaleCitations(content string, roots []string) []VerifierFinding {
	matches := citationRE.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	var findings []VerifierFinding
	for _, m := range matches {
		path, lineStr := m[1], m[2]
		line, err := strconv.Atoi(lineStr)
		if err != nil || line <= 0 {
			continue
		}
		// Skip purely-version-string false positives like "v1.2:3".
		// Real citations carry a path separator OR an extension we
		// recognise; the regex already requires "<something>.<ext>"
		// but very short extension matches ("v1.2:3" has ext "2")
		// slip through. Filter on extension length >= 2 AND
		// non-numeric.
		if !looksLikePath(path) {
			continue
		}
		resolved, found := resolveCitation(path, roots)
		if !found {
			findings = append(findings, VerifierFinding{
				CheckName: "stale_citation",
				Detail: map[string]any{
					"path":   path,
					"line":   line,
					"reason": "file not found under any CitationSearchRoots",
				},
			})
			continue
		}
		// Range check: load file and count newlines.
		if exceeded, err := lineExceedsFile(resolved, line); err != nil {
			findings = append(findings, VerifierFinding{
				CheckName: "stale_citation",
				Detail: map[string]any{
					"path":     path,
					"line":     line,
					"resolved": resolved,
					"reason":   "read error: " + err.Error(),
				},
			})
		} else if exceeded {
			findings = append(findings, VerifierFinding{
				CheckName: "stale_citation",
				Detail: map[string]any{
					"path":     path,
					"line":     line,
					"resolved": resolved,
					"reason":   "line number exceeds file length",
				},
			})
		}
	}
	return findings
}

// looksLikePath weeds out false-positives from the conservative regex.
// A real citation has at least one slash OR a multi-char extension
// that isn't purely numeric (so "v1.2" misses, "main.go" matches).
func looksLikePath(s string) bool {
	if strings.Contains(s, "/") {
		return true
	}
	dot := strings.LastIndex(s, ".")
	if dot < 0 || dot == len(s)-1 {
		return false
	}
	ext := s[dot+1:]
	if len(ext) < 2 {
		return false
	}
	// Numeric "extension" is a version segment, not a file ext.
	for _, r := range ext {
		if r < '0' || r > '9' {
			return true
		}
	}
	return false
}

// resolveCitation tries each search root in order, returning the
// first absolute path whose Stat succeeds. Symlinks are followed
// implicitly via os.Stat.
func resolveCitation(rel string, roots []string) (string, bool) {
	// Absolute paths are taken at face value — useful for /workspace
	// style bind-mount references.
	if filepath.IsAbs(rel) {
		if _, err := os.Stat(rel); err == nil {
			return rel, true
		}
		return "", false
	}
	for _, root := range roots {
		full := filepath.Join(root, rel)
		if _, err := os.Stat(full); err == nil {
			return full, true
		}
	}
	return "", false
}

// lineExceedsFile reads the file just enough to count newlines.
// Returns true when `line` > number of lines in file. Errors on
// read failure (caller treats it as stale_citation).
//
// For large files we could fseek to a sliding window, but real
// citations point at code/markdown — kilobytes, not megabytes —
// so a full ReadFile is fine.
func lineExceedsFile(path string, line int) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	// Counting '\n' is the line count - 1 for a file without
	// trailing newline; we want "addressable line count" which
	// is the count of newlines + (1 if file is non-empty), i.e.
	// strings.Count + bool flag.
	count := strings.Count(string(data), "\n")
	if len(data) > 0 && data[len(data)-1] != '\n' {
		count++
	}
	return line > count, nil
}

// Wire-into WriteFile: implemented in writer.go via a new field on
// WriteConfig. Documented here so the next reader sees the loop.
var _ = fmt.Sprintf // keep fmt import live; future Finding-formatting hook
