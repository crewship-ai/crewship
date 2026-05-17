package memory

import (
	"context"
	"os"
	"path/filepath"
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
	res, _ := VerifyWrite(context.Background(),
		[]byte("see auth.go:2 and missing.go:1 for context"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{root}})
	if res.Decision != VerifierReject {
		t.Fatalf("mixed pass+fail should reject, got %v", res.Decision)
	}
}

func TestVerifyWrite_VersionStrings_NotMatched(t *testing.T) {
	// "v1.2:3" looks like a citation to a naive regex but isn't a
	// real path. looksLikePath filters numeric-only "extensions".
	res, _ := VerifyWrite(context.Background(),
		[]byte("released v1.2:3 yesterday"),
		VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{t.TempDir()}})
	if res.Decision != VerifierAllow {
		t.Errorf("version string should not trigger citation check, got %v", res.Decision)
	}
}

func TestVerifyWrite_NoSearchRoots_NoOp(t *testing.T) {
	// Without configured roots the citation check is silently skipped
	// — operator opted into cheap mode but didn't supply paths.
	res, _ := VerifyWrite(context.Background(),
		[]byte("see auth/login.go:42"),
		VerifierConfig{Mode: VerifierCheap})
	if res.Decision != VerifierAllow {
		t.Errorf("no roots configured should silently allow, got %v", res.Decision)
	}
}

func TestVerifyWrite_LLMMode_DegradesToCheapWithFinding(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.go"), []byte("a\nb\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, _ := VerifyWrite(context.Background(),
		[]byte("see ok.go:1"),
		VerifierConfig{Mode: VerifierLLM, CitationSearchRoots: []string{root}})
	if res.Decision != VerifierAllow {
		t.Errorf("LLM mode with valid citation should allow, got %v", res.Decision)
	}
	// llm_skipped finding present so audit shows the mode degraded.
	found := false
	for _, f := range res.Findings {
		if f.CheckName == "llm_skipped" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected llm_skipped Finding in LLM mode")
	}
}

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
	got, _ := os.ReadFile(path)
	if string(got) != "notes: see src.go:2" {
		t.Errorf("on-disk content mismatch: %q", got)
	}
}
