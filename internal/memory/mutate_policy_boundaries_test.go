package memory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory/memdiff"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

func TestMutationValidationFailsBeforeCreatingIntentOrTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*MutateRequest)
	}{
		{"operation", func(r *MutateRequest) { r.OperationID = " " }},
		{"path", func(r *MutateRequest) { r.Path = " " }},
		{"audit path", func(r *MutateRequest) { r.AuditPath = " " }},
		{"invalid operation", func(r *MutateRequest) { r.Op = "delete" }},
		{"workspace", func(r *MutateRequest) { r.WorkspaceID = " " }},
		{"blob root", func(r *MutateRequest) { r.BlobRoot = " " }},
		{"negative revision", func(r *MutateRequest) { r.ExpectedRevision = -1 }},
		{"version tier", func(r *MutateRequest) { r.RecordVersion = true; r.Tier = "invalid" }},
		{"contradictory first write", func(r *MutateRequest) {
			r.Profile = ProfileGuaranteed
			r.Op = OpReplace
			r.FirstWrite = true
			r.ExpectedRevision = 1
			r.Removals = []memdiff.Removal{}
			r.Authorize = func(context.Context) error { return nil }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutateFixture(t)
			req := f.req("op", OpAppend, "fact\n")
			tc.change(&req)
			if _, err := f.mutate(t, req); err == nil {
				t.Fatal("invalid mutation accepted")
			}
			var count int
			if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_mutations`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid request wrote intent: %d %v", count, err)
			}
			if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid request wrote target: %v", err)
			}
		})
	}
}

func TestMutationScrubberAndVerifierPoliciesPrecedeDurableWrite(t *testing.T) {
	const sensitive = "AKIAIOSFODNN7EXAMPLE"
	for _, kind := range []string{"block", "redact", "warn", "allow", "verifier", "screen error"} {
		t.Run(kind, func(t *testing.T) {
			f := newMutateFixture(t)
			req := f.req("policy", OpAppend, sensitive+"\n")
			req.Cfg.Scrubber = scrubber.New()
			switch kind {
			case "block":
				req.Cfg.ScrubberMode = scrubber.ModeBlock
			case "redact":
				req.Cfg.ScrubberMode = scrubber.ModeRedact
			case "warn":
				req.Cfg.ScrubberMode = scrubber.ModeWarn
			case "allow":
				req.Cfg.ScrubberMode = scrubber.ModeBlock
				req.Content = "ordinary fact\n"
			case "verifier":
				req.Cfg.Scrubber = nil
				req.Content = "see missing.go:42\n"
				req.Cfg.Verifier = VerifierConfig{Mode: VerifierCheap, CitationSearchRoots: []string{t.TempDir()}}
			case "screen error":
				req.Screen = func(context.Context, []byte) (*MutateRejection, error) {
					return nil, errors.New("fixture policy unavailable")
				}
			}
			result, err := f.mutate(t, req)
			if kind == "screen error" {
				if err == nil || !strings.Contains(err.Error(), "fixture policy unavailable") {
					t.Fatalf("screen error hidden: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			denied := kind == "block" || kind == "verifier" || kind == "screen error"
			if denied {
				if kind != "screen error" && result.Rejection == nil {
					t.Fatalf("policy accepted content: %+v", result)
				}
				if _, err := os.Stat(f.path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("rejected content written: %v", err)
				}
				var count int
				if err := f.db.QueryRowContext(t.Context(), `SELECT count(*) FROM memory_mutations`).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rejection wrote intent: %d %v", count, err)
				}
				return
			}
			if result.Rejection != nil {
				t.Fatalf("allowed policy rejected: %+v", result.Rejection)
			}
			body := f.onDisk(t)
			if kind == "redact" {
				if strings.Contains(body, sensitive) || body == "" {
					t.Fatalf("redaction failed: %q", body)
				}
			} else if body != req.Content {
				t.Fatalf("allowed content changed: %q", body)
			}
			if result.ContentSHA256 != sha256Hex([]byte(body)) || string(result.Written) != body {
				t.Fatal("ledger hash/result differs from policy output")
			}
		})
	}
}

func TestMutationContextAndNoopRevision(t *testing.T) {
	f := newMutateFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := Mutate(ctx, f.db, f.req("cancelled", OpAppend, "fact\n")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write accepted: %v", err)
	}
	first, err := Mutate(t.Context(), f.db, f.req("initial", OpAppend, "fact\n"))
	if err != nil {
		t.Fatal(err)
	}
	req := f.req("same-content-new-operation", OpReplace, "fact\n")
	req.ExpectedRevision = first.Revision
	req.Removals = []memdiff.Removal{}
	same := f.mustMutate(t, req)
	if same.Revision != first.Revision || same.Idempotent {
		t.Fatalf("unchanged content bumped revision or lost operation identity: %+v", same)
	}
}
