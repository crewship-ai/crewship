//go:build linux

package restrictedworkflow

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

func TestWorkflowReceiptRejectsCorruptFrozenAuthority(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	original, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := encryption.Decrypt(original.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.checkJob(t.Context(), s.db, original, handle); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*job)
	}{
		{"malformed expiration", func(j *job) { j.Expires = "invalid" }},
		{"expired pending", func(j *job) { j.Expires = "2000-01-01T00:00:00Z" }},
		{"recipe hash", func(j *job) { j.RecipeHash = "changed" }},
		{"rights JSON", func(j *job) { j.Rights = "{" }},
		{"rights empty id", func(j *job) { j.Rights = `[{"kind":"agent","operation":"run","id":""}]` }},
		{"rights unknown kind", func(j *job) { j.Rights = `[{"kind":"credential","operation":"read","id":"key"}]` }},
		{"rights write project", func(j *job) { j.Rights = `[{"kind":"project","operation":"write","id":"project"}]` }},
		{"rights unavailable project", func(j *job) { j.Rights = `[{"kind":"project","operation":"read","id":"missing"}]` }},
		{"rights unavailable agent", func(j *job) { j.Rights = `[{"kind":"agent","operation":"run","id":"missing"}]` }},
		{"source facet", func(j *job) { j.SourceFacet = "unknown" }},
		{"source kind", func(j *job) { j.Source = "unknown" }},
		{"page without binding", func(j *job) { j.Source = "page" }},
		{"missing graph", func(j *job) { j.Graph = "" }},
		{"oversized graph", func(j *job) { j.Graph = strings.Repeat("x", (1<<20)+1) }},
		{"graph hash", func(j *job) { j.GraphHash = "changed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := original
			tc.mutate(&j)
			if err := s.checkJob(t.Context(), s.db, j, handle); !errors.Is(err, ErrDenied) {
				t.Fatalf("corrupt authority accepted: %v", err)
			}
		})
	}
	if got, err := s.ReceiptForActor(t.Context(), "h1", "w", receipt.ID); err != nil || got.ID != receipt.ID || got.State != "pending" {
		t.Fatalf("valid receipt changed: %+v %v", got, err)
	}
	for _, who := range []struct{ user, ws, id string }{{"h2", "w", receipt.ID}, {"h1", "elsewhere", receipt.ID}, {"h1", "w", "missing"}} {
		if _, err := s.ReceiptForActor(t.Context(), who.user, who.ws, who.id); !errors.Is(err, ErrDenied) {
			t.Fatalf("foreign receipt disclosed: %v", err)
		}
	}
}

func TestWorkflowOutputProofEnvelopeRejectsTampering(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	if worked, err := s.DispatchNext(t.Context()); !worked || err != nil {
		t.Fatalf("dispatch %v %v", worked, err)
	}
	original, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*job)
	}{
		{"invalid graph", func(j *job) { j.Graph = "{}" }},
		{"malformed proof", func(j *job) { j.Proofs = "{" }},
		{"empty proof", func(j *job) { j.Proofs = "{}" }},
		{"oversized proof", func(j *job) { j.Proofs = strings.Repeat("x", (256<<10)+1) }},
		{"foreign agent", func(j *job) {
			var v map[string]sealedLeaf
			_ = json.Unmarshal([]byte(j.Proofs), &v)
			v["0"] = sealedLeaf{Agent: "foreign", Cipher: v["0"].Cipher}
			raw, _ := json.Marshal(v)
			j.Proofs = string(raw)
		}},
		{"invalid ciphertext", func(j *job) {
			var v map[string]sealedLeaf
			_ = json.Unmarshal([]byte(j.Proofs), &v)
			v["0"] = sealedLeaf{Agent: v["0"].Agent, Cipher: "not-a-proof"}
			raw, _ := json.Marshal(v)
			j.Proofs = string(raw)
		}},
		{"missing leaf", func(j *job) {
			var v map[string]sealedLeaf
			_ = json.Unmarshal([]byte(j.Proofs), &v)
			delete(v, "0")
			raw, _ := json.Marshal(v)
			j.Proofs = string(raw)
		}},
		{"extra leaf", func(j *job) {
			var v map[string]sealedLeaf
			_ = json.Unmarshal([]byte(j.Proofs), &v)
			v["unknown"] = v["0"]
			raw, _ := json.Marshal(v)
			j.Proofs = string(raw)
		}},
		{"output projection mismatch", func(j *job) { j.Outputs = `{"first":"forged private output"}` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := original
			tc.mutate(&j)
			if out, err := s.readGraphOutputs(t.Context(), j); out != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("unproven output released: %#v %v", out, err)
			}
		})
	}
	if out, err := s.readGraphOutputs(t.Context(), original); err != nil || len(out) != 2 {
		t.Fatalf("valid output rejected: %#v %v", out, err)
	}
}

func TestWorkflowAdmissionRejectsInvalidEnvelopeWithoutQueuedWork(t *testing.T) {
	s, _ := fixture(t)
	action := seedPage(t, s)
	for _, delay := range []time.Duration{-time.Second, 25 * time.Hour} {
		if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", delay); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid delay accepted: %v", err)
		}
	}
	if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", nil, "", 0, "one", "two"); !errors.Is(err, ErrDenied) {
		t.Fatalf("ambiguous idempotency accepted: %v", err)
	}
	if _, err := s.AdmitPage(t.Context(), "h1", "w", action, nil, "one", "two"); !errors.Is(err, ErrDenied) {
		t.Fatalf("ambiguous Page idempotency accepted: %v", err)
	}
	if _, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "private"}, "", 0, strings.Repeat("x", 129)); !errors.Is(err, ErrDenied) {
		t.Fatalf("oversized key accepted: %v", err)
	}
	if _, err := s.AdmitPageFrozen(t.Context(), "h1", "w", action, map[string]any{"task": "private"}, "obsolete-intent"); !errors.Is(err, ErrDenied) {
		t.Fatalf("obsolete intent accepted: %v", err)
	}
	for _, table := range []string{"restricted_workflow_jobs", "work_items"} {
		var count int
		if err := s.db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("invalid request queued: %s %d %v", table, count, err)
		}
	}
}

func TestWorkflowClosedStorageAndCancelledReceiptFailClosed(t *testing.T) {
	s, _ := fixture(t)
	receipt := admitFixtureJob(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ReceiptForActor(ctx, "h1", "w", receipt.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("cancelled receipt read: %v", err)
	}
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Catalog(t.Context(), "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("closed catalog: %v", err)
	}
	if _, err := s.ResultsForActor(t.Context(), "h1", "w"); !errors.Is(err, ErrDenied) {
		t.Fatalf("closed results: %v", err)
	}
	if err := s.runtime.FlushRunOutcomes(t.Context()); err == nil {
		t.Fatal("closed projection reported success")
	}
}
