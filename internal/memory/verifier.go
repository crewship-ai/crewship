package memory

import (
	"bufio"
	"context"
	"fmt"
	"io"
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
	// PinnedFacts is the list of operator-pinned facts the LLM
	// verifier should treat as ground truth. The verifier rejects
	// any candidate write that contradicts any pinned fact.
	// Empty list disables the contradiction check even in
	// VerifierLLM mode.
	PinnedFacts []string
	// LLM is the pluggable LLM verifier used in VerifierLLM mode.
	// Nil falls back to a "skipped" finding so VerifierLLM
	// degrades to VerifierCheap when no LLM client is wired
	// (test harness, dev mode without Ollama). The interface
	// stays narrow so a future swap from Ollama-phi3 to Anthropic
	// Haiku is one constructor change.
	LLM LLMVerifier
}

// LLMVerifier is the narrow surface VerifyWrite uses for the
// contradiction check in VerifierLLM mode. Implementations call a
// cheap LLM with the candidate content + the list of pinned facts
// and return whether the content contradicts any pin.
//
// Contract:
//
//   - `pinnedFacts` is non-empty; the caller skips the LLM hop
//     entirely when there's nothing to compare against.
//   - `reason` carries the LLM's free-text explanation, surfaced in
//     the rejection envelope for operator review. Empty on
//     contradicts=false.
//   - Errors should be returned verbatim; VerifyWrite logs them
//     and degrades to cheap-only rather than failing the whole
//     write. Hard-failing on LLM errors would couple memory
//     writes to LLM uptime, which is exactly what the security
//     literature (arXiv 2601.05504) warns against.
type LLMVerifier interface {
	VerifyContradiction(ctx context.Context, content []byte, pinnedFacts []string) (contradicts bool, reason string, err error)
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

	// VerifierLLM mode: run the LLM contradiction check first so
	// a contradiction Reject takes priority over citation Reject
	// (operator-pinned facts are the strongest signal). Falls back
	// gracefully when no LLM client is wired or no pinned facts
	// are supplied — both surface as a "llm_skipped" Finding so
	// audit reviewers see the mode was requested but didn't fire.
	if cfg.Mode == VerifierLLM {
		switch {
		case cfg.LLM == nil:
			result.Findings = append(result.Findings, VerifierFinding{
				CheckName: "llm_skipped",
				Detail:    map[string]any{"reason": "no LLM verifier wired"},
			})
		case len(cfg.PinnedFacts) == 0:
			result.Findings = append(result.Findings, VerifierFinding{
				CheckName: "llm_skipped",
				Detail:    map[string]any{"reason": "no pinned facts to compare against"},
			})
		default:
			contradicts, reason, llmErr := cfg.LLM.VerifyContradiction(ctx, content, cfg.PinnedFacts)
			if llmErr != nil {
				// Degrade: log via the Finding (caller's logger
				// picks it up), don't reject. Coupling memory
				// writes to LLM uptime defeats the verifier's
				// purpose.
				result.Findings = append(result.Findings, VerifierFinding{
					CheckName: "llm_error",
					Detail:    map[string]any{"error": llmErr.Error()},
				})
			} else if contradicts {
				result.Findings = append(result.Findings, VerifierFinding{
					CheckName: "contradiction",
					Detail: map[string]any{
						"reason":       reason,
						"pinned_count": len(cfg.PinnedFacts),
					},
				})
				result.Decision = VerifierReject
				result.Kind = "contradiction"
				// Short-circuit: a contradiction is a stronger
				// rejection signal than a stale citation, so we
				// return now rather than potentially overwriting
				// the kind with "stale_citation" below.
				return result, nil
			} else {
				// Allow path with a positive Finding so the audit
				// trail records "LLM verified, no contradiction".
				result.Findings = append(result.Findings, VerifierFinding{
					CheckName: "contradiction_clean",
					Detail:    map[string]any{"reason": reason},
				})
			}
		}
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
// first path whose Stat succeeds AND which sits inside one of the
// configured roots. Both absolute and relative citations are confined
// to roots — without confinement, an absolute "/etc/passwd" would
// validate against the host filesystem and leak filesystem metadata
// (existence, size) through the verifier's rejection details.
func resolveCitation(rel string, roots []string) (string, bool) {
	if len(roots) == 0 {
		return "", false
	}
	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		candidate := rel
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(absRoot, rel)
		}
		absCandidate, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		// Containment check: separator-anchored prefix so a citation
		// like "/var/lib-evil/x" cannot escape "/var/lib".
		rootWithSep := filepath.Clean(absRoot) + string(filepath.Separator)
		cleanCand := filepath.Clean(absCandidate) + string(filepath.Separator)
		if !strings.HasPrefix(cleanCand, rootWithSep) {
			continue
		}
		if _, err := os.Stat(absCandidate); err == nil {
			return absCandidate, true
		}
	}
	return "", false
}

// lineExceedsFile streams the file and counts newlines.
// Returns true when `line` > number of lines in file. Errors on
// read failure (caller treats it as stale_citation).
//
// Streaming defends against the user-controlled-input DoS shape:
// every memory write surfaces citations through the verifier, and
// loading multi-MB files into RAM per write would let a tampered
// citation block reads with a memory spike. bufio.Scanner walks the
// file with a constant-size buffer; we short-circuit once the count
// passes `line` so verifying a citation against a 100MB log is
// O(line bytes), not O(filesize).
func lineExceedsFile(path string, line int) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	// Use ReadByte through bufio.Reader rather than Scanner so we
	// don't allocate per-line strings just to discard them.
	br := bufio.NewReader(f)
	var count int
	var sawAny bool
	for {
		b, err := br.ReadByte()
		if err == io.EOF {
			// File ends without trailing newline: count the trailing
			// fragment as an addressable line.
			if sawAny && b != '\n' {
				count++
			}
			return line > count, nil
		}
		if err != nil {
			return false, err
		}
		sawAny = true
		if b == '\n' {
			count++
			if count >= line {
				// Early-exit: we've already reached the cited line,
				// so the citation is NOT out of range regardless of
				// what follows.
				return false, nil
			}
		}
	}
}

// Wire-into WriteFile: implemented in writer.go via a new field on
// WriteConfig. Documented here so the next reader sees the loop.
var _ = fmt.Sprintf // keep fmt import live; future Finding-formatting hook
