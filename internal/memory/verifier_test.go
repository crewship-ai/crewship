package memory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyWrite_Off_AlwaysAllow(t *testing.T) {
	res, err := VerifyWrite(context.Background(),
		[]byte("any content with see file.go:9999999 here"),
		VerifierConfig{Mode: VerifierOff})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("Off mode should always allow, got %v", res.Decision)
	}
}

func TestVerifyWrite_Cheap_NoCitations_Allow(t *testing.T) {
	res, err := VerifyWrite(context.Background(),
		[]byte("memory body with no citations to validate"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("no-citation content should allow, got %v", res.Decision)
	}
}

func TestVerifyWrite_StaleCitation_FileMissing(t *testing.T) {
	root := t.TempDir()
	// Citation references a file that doesn't exist under the root.
	res, err := VerifyWrite(context.Background(),
		[]byte("see auth/login.go:42 for context"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{root}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierReject {
		t.Fatalf("missing file should reject, got %v", res.Decision)
	}
	if res.Kind != "stale_citation" {
		t.Errorf("Kind = %q, want stale_citation", res.Kind)
	}
	if len(res.Findings) != 1 {
		t.Errorf("Findings len = %d, want 1", len(res.Findings))
	}
}

func TestVerifyWrite_StaleCitation_LineOutOfRange(t *testing.T) {
	root := t.TempDir()
	// File exists with 5 lines; citation points at line 100.
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := VerifyWrite(context.Background(),
		[]byte("auth.go:100 has the bug"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{root}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierReject {
		t.Fatalf("out-of-range line should reject, got %v", res.Decision)
	}
	if res.Findings[0].Detail["reason"] != "line number exceeds file length" {
		t.Errorf("Finding reason = %v, want line-exceeds", res.Findings[0].Detail["reason"])
	}
}

func TestVerifyWrite_ValidCitation_Allow(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := VerifyWrite(context.Background(),
		[]byte("see auth.go:3 — the validate call"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{root}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("valid in-range citation should allow, got %v", res.Decision)
	}
}

func TestVerifyWrite_MixedCitations_AnyStaleRejects(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// First citation valid; second points at non-existent file.
	// Even one stale citation flips the decision to Reject.
	res, err := VerifyWrite(context.Background(),
		[]byte("see auth.go:2 and missing.go:1 for context"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{root}})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierReject {
		t.Fatalf("mixed pass+fail should reject, got %v", res.Decision)
	}
}

func TestVerifyWrite_VersionStrings_NotMatched(t *testing.T) {
	// "v1.2:3" looks like a citation to a naive regex but isn't a
	// real path. looksLikePath filters numeric-only "extensions".
	res, err := VerifyWrite(context.Background(),
		[]byte("released v1.2:3 yesterday"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("version string should not trigger citation check, got %v", res.Decision)
	}
}

func TestVerifyWrite_NoSearchRoots_NoOp(t *testing.T) {
	// Without configured roots the citation check is silently skipped
	// — operator opted into cheap mode but didn't supply paths.
	res, err := VerifyWrite(context.Background(),
		[]byte("see auth/login.go:42"),
		VerifierConfig{Mode: VerifierCheap})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("no roots configured should silently allow, got %v", res.Decision)
	}
}

func TestVerifyWrite_LLMMode_NoLLM_SkippedFinding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.go"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := VerifyWrite(context.Background(),
		[]byte("see ok.go:1"),
		VerifierConfig{Mode: VerifierLLM, CitationSearchRoots: []string{root}})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("LLM mode without LLM should allow, got %v", res.Decision)
	}
	found := false
	for _, f := range res.Findings {
		if f.CheckName == "llm_skipped" {
			found = true
			if reason, _ := f.Detail["reason"].(string); reason != "no LLM verifier wired" {
				t.Errorf("skipped reason = %q, want 'no LLM verifier wired'", reason)
			}
			break
		}
	}
	if !found {
		t.Errorf("expected llm_skipped Finding in LLM mode without LLM")
	}
}

// stubLLMVerifier returns canned (contradicts, reason) so the LLM
// integration tests don't need a real Ollama. Calls are recorded
// for assertion.
type stubLLMVerifier struct {
	contradicts bool
	reason      string
	err         error
	calls       int
}

func (s *stubLLMVerifier) VerifyContradiction(_ context.Context, content []byte, pinnedFacts []string) (bool, string, error) {
	s.calls++
	return s.contradicts, s.reason, s.err
}

func TestVerifyWrite_LLMMode_Contradiction_Rejects(t *testing.T) {
	llm := &stubLLMVerifier{contradicts: true, reason: "candidate says X; pin says NOT X"}
	res, err := VerifyWrite(context.Background(),
		[]byte("any candidate body"),
		VerifierConfig{
			Mode:        VerifierLLM,
			LLM:         llm,
			PinnedFacts: []string{"NOT X is the established truth"},
		})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Decision != VerifierReject {
		t.Fatalf("contradiction should reject, got %v", res.Decision)
	}
	if res.Kind != "contradiction" {
		t.Errorf("Kind = %q, want contradiction", res.Kind)
	}
	if llm.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", llm.calls)
	}
	// Reason wired through to the Finding.
	found := false
	for _, f := range res.Findings {
		if f.CheckName == "contradiction" {
			found = true
			if r, _ := f.Detail["reason"].(string); !strings.Contains(r, "NOT X") {
				t.Errorf("contradiction reason missing LLM detail: %v", f.Detail)
			}
		}
	}
	if !found {
		t.Errorf("expected 'contradiction' Finding")
	}
}

func TestVerifyWrite_LLMMode_NoContradiction_Allow(t *testing.T) {
	llm := &stubLLMVerifier{contradicts: false, reason: "no overlap"}
	res, err := VerifyWrite(context.Background(),
		[]byte("unrelated body"),
		VerifierConfig{Mode: VerifierLLM, LLM: llm, PinnedFacts: []string{"X is true"}})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("no-contradiction should allow, got %v", res.Decision)
	}
	// Positive Finding records the clean check — audit shows the
	// LLM ran even when it found nothing.
	hasClean := false
	for _, f := range res.Findings {
		if f.CheckName == "contradiction_clean" {
			hasClean = true
		}
	}
	if !hasClean {
		t.Errorf("expected contradiction_clean Finding on no-contradiction allow")
	}
}

func TestVerifyWrite_LLMMode_EmptyPinnedFacts_Skipped(t *testing.T) {
	llm := &stubLLMVerifier{}
	res, err := VerifyWrite(context.Background(),
		[]byte("anything"),
		VerifierConfig{Mode: VerifierLLM, LLM: llm, PinnedFacts: nil})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("empty pins should allow, got %v", res.Decision)
	}
	if llm.calls != 0 {
		t.Errorf("LLM should NOT be called with empty pins; calls=%d", llm.calls)
	}
	hasSkipped := false
	for _, f := range res.Findings {
		if f.CheckName == "llm_skipped" {
			hasSkipped = true
			if reason, _ := f.Detail["reason"].(string); reason != "no pinned facts to compare against" {
				t.Errorf("skipped reason = %q, want 'no pinned facts...'", reason)
			}
		}
	}
	if !hasSkipped {
		t.Errorf("expected llm_skipped Finding when PinnedFacts empty")
	}
}

func TestVerifyWrite_LLMMode_LLMError_DegradesToAllow(t *testing.T) {
	llm := &stubLLMVerifier{err: errStubLLMUnavailable}
	res, err := VerifyWrite(context.Background(),
		[]byte("anything"),
		VerifierConfig{Mode: VerifierLLM, LLM: llm, PinnedFacts: []string{"X"}})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierAllow {
		t.Errorf("LLM error should degrade to allow (not block writes on LLM downtime), got %v", res.Decision)
	}
	hasError := false
	for _, f := range res.Findings {
		if f.CheckName == "llm_error" {
			hasError = true
		}
	}
	if !hasError {
		t.Errorf("expected llm_error Finding on LLM call failure")
	}
}

// TestVerifyWrite_LLMMode_ContradictionTrumpStaleCitation: when both
// the LLM finds a contradiction AND a stale citation exists, the
// contradiction Reject takes priority and the function returns early
// — operator sees the strongest signal first.
func TestVerifyWrite_LLMMode_ContradictionTrumpStaleCitation(t *testing.T) {
	llm := &stubLLMVerifier{contradicts: true, reason: "conflicts with pin"}
	root := t.TempDir() // empty — citation would also be stale
	res, err := VerifyWrite(context.Background(),
		[]byte("see missing.go:42 (stale citation)"),
		VerifierConfig{
			Mode:                VerifierLLM,
			LLM:                 llm,
			PinnedFacts:         []string{"X is established"},
			CitationSearchRoots: []string{root},
		})
	if err != nil {
		t.Fatalf("VerifyWrite: %v", err)
	}
	if res.Decision != VerifierReject {
		t.Fatalf("should reject, got %v", res.Decision)
	}
	if res.Kind != "contradiction" {
		t.Errorf("Kind = %q, want contradiction (contradiction trumps stale_citation)", res.Kind)
	}
}

// errStubLLMUnavailable mirrors a real Ollama-down error shape so
// the degradation test reads like production.
var errStubLLMUnavailable = stubErr("llm unavailable: connection refused")

type stubErr string

func (e stubErr) Error() string { return string(e) }

func TestWriteFile_VerifierRejection_NoFilesystemWrite(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir() // empty — no files for citations to resolve
	path := filepath.Join(dir, "AGENT.md")
	res, err := WriteFile(context.Background(), path,
		[]byte("see missing.go:42 (this will fail verifier)"),
		WriteConfig{
			Verifier: VerifierConfig{
				Mode:                VerifierCheap,
				CitationSearchRoots: []string{root},
			},
		})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !res.Rejected {
		t.Fatalf("expected Rejected=true on verifier fail, got %+v", res)
	}
	if res.RejectionKind != "verifier" {
		t.Errorf("RejectionKind = %q, want verifier", res.RejectionKind)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("file should not exist after verifier rejection, stat err=%v", err)
	}
}

func TestWriteFile_VerifierAllow_WritesAsUsual(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "src.go"), []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	path := filepath.Join(dir, "AGENT.md")
	res, err := WriteFile(context.Background(), path,
		[]byte("notes: see src.go:2"),
		WriteConfig{
			Verifier: VerifierConfig{
				Mode:                VerifierCheap,
				CitationSearchRoots: []string{root},
			},
		})
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if res.Rejected {
		t.Fatalf("should allow valid citation, got %+v", res)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "notes: see src.go:2" {
		t.Errorf("on-disk content mismatch: %q", got)
	}
}
