package api

// #1825 — the journal must be able to answer "which image digest did this run
// execute under?".
//
// The digest is resolved deep in the container provider, which has no journal
// dependency (and should not grow one). It travels out as a ProvisionEvent
// field, and this file pins the two things that have to be true at the
// routing point for the record to be worth anything:
//
//   - the digest and its pinned/unpinned qualifier reach the journal payload,
//   - the row carries the RUN's trace_id, so "which image did THIS run use?"
//     is a lookup rather than a correlation-by-timestamp guess.
//
// Deliberately asserted against the persisted row, not against the emitter
// call, because the payload is what the tamper-evident chain (#1369, v152)
// actually hashes.

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/journal"
)

const testImageDigest = "sha256:9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f9f"

// readProvisionStepRow returns the payload and trace_id of the single
// provisioning.step journal row for a crew.
func readProvisionStepRow(t *testing.T, h *ProvisioningHandler, crewID string) (map[string]any, string) {
	t.Helper()
	var payloadJSON, traceID string
	err := h.db.QueryRow(
		`SELECT payload, COALESCE(trace_id, '') FROM journal_entries
		  WHERE crew_id = ? AND entry_type = 'provisioning.step'
		  ORDER BY ts DESC LIMIT 1`, crewID).Scan(&payloadJSON, &traceID)
	if err != nil {
		t.Fatalf("no provisioning.step row for crew %s: %v", crewID, err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload is not JSON (%q): %v", payloadJSON, err)
	}
	return payload, traceID
}

func newDigestSinkHandler(t *testing.T) (*ProvisioningHandler, *journal.Writer, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewProvisioningHandler(db, logger, nil, nil, nil, nil, "", nil)
	t.Cleanup(h.Stop)

	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := "crew-1825"
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Eng', 'eng-1825')`, crewID, wsID)

	w := journal.NewWriter(db, logger, journal.WriterOptions{})
	h.SetJournal(w)
	return h, w, wsID, crewID
}

// TestRuntimeProvisionSink_RecordsImageDigest is the core assertion: a pinned
// image_resolved event lands in the journal with its digest.
func TestRuntimeProvisionSink_RecordsImageDigest(t *testing.T) {
	h, w, wsID, crewID := newDigestSinkHandler(t)

	runID := "run-1825-abcdef"
	ctx := journal.WithRunID(context.Background(), runID)

	sink := h.RuntimeProvisionSink(ctx, crewID, wsID)
	sink(devcontainer.ProvisionEvent{
		Phase:  devcontainer.ProvisionPhase,
		Step:   devcontainer.ProvStepImageResolved,
		Status: devcontainer.ProvStatusCompleted,
		Tag:    "ghcr.io/crewship-ai/agent-runtime:latest",
		Digest: testImageDigest,
		Pinned: true,
	})
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload, traceID := readProvisionStepRow(t, h, crewID)

	if got := payload["digest"]; got != testImageDigest {
		t.Errorf("payload[digest] = %v, want %q — the journal cannot say which image ran", got, testImageDigest)
	}
	if got := payload["pinned"]; got != true {
		t.Errorf("payload[pinned] = %v, want true — a pinned pull must be distinguishable from a tag pull", got)
	}
	if got := payload["tag"]; got != "ghcr.io/crewship-ai/agent-runtime:latest" {
		t.Errorf("payload[tag] = %v, want the tag preserved alongside the digest", got)
	}
	if traceID != runID {
		t.Errorf("trace_id = %q, want %q — the digest must be attributable to the run, not just the crew", traceID, runID)
	}
}

// TestRuntimeProvisionSink_UnpinnedIsExplicit guards the honest-absence rule.
// A digest we could not pin must not read the same as one we did: `pinned`
// has to be present and false, and it must never be inferred from the digest
// being non-empty.
func TestRuntimeProvisionSink_UnpinnedIsExplicit(t *testing.T) {
	h, w, wsID, crewID := newDigestSinkHandler(t)

	sink := h.RuntimeProvisionSink(context.Background(), crewID, wsID)
	sink(devcontainer.ProvisionEvent{
		Phase:  devcontainer.ProvisionPhase,
		Step:   devcontainer.ProvStepImageResolved,
		Status: devcontainer.ProvStatusCompleted,
		Tag:    "ghcr.io/crewship-ai/agent-runtime:latest",
		Digest: testImageDigest,
		Pinned: false,
	})
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload, _ := readProvisionStepRow(t, h, crewID)

	if got, ok := payload["pinned"]; !ok || got != false {
		t.Errorf("payload[pinned] = %v (present=%v), want an explicit false", got, ok)
	}
	if got := payload["digest"]; got != testImageDigest {
		t.Errorf("payload[digest] = %v, want %q", got, testImageDigest)
	}
}

// TestRuntimeProvisionSink_NoDigestOmitsTheKey covers the image that has no
// registry digest (a locally-built crewship-cache:* derivative). Writing
// "digest": "" would let a reader mistake "no such thing" for "we lost it";
// the key is simply absent.
func TestRuntimeProvisionSink_NoDigestOmitsTheKey(t *testing.T) {
	h, w, wsID, crewID := newDigestSinkHandler(t)

	sink := h.RuntimeProvisionSink(context.Background(), crewID, wsID)
	sink(devcontainer.ProvisionEvent{
		Phase:  devcontainer.ProvisionPhase,
		Step:   devcontainer.ProvStepImageResolved,
		Status: devcontainer.ProvStatusCompleted,
		Tag:    "crewship-cache:0d08da4b8ac3",
	})
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload, _ := readProvisionStepRow(t, h, crewID)

	if _, ok := payload["digest"]; ok {
		t.Errorf("payload[digest] present (%v) for an image with no registry digest; want the key omitted", payload["digest"])
	}
	if got := payload["tag"]; got != "crewship-cache:0d08da4b8ac3" {
		t.Errorf("payload[tag] = %v, want the cache tag recorded even without a digest", got)
	}
}

// TestRuntimeProvisionSink_SurvivesCancelledRunContext protects the durability
// property the sink already had. Threading the run's ctx through for its
// trace_id must NOT reintroduce cancellation: a run that ends (or is
// cancelled) the moment its container comes up still owes an audit row.
func TestRuntimeProvisionSink_SurvivesCancelledRunContext(t *testing.T) {
	h, w, wsID, crewID := newDigestSinkHandler(t)

	runID := "run-1825-cancelled"
	ctx, cancel := context.WithCancel(journal.WithRunID(context.Background(), runID))
	sink := h.RuntimeProvisionSink(ctx, crewID, wsID)
	cancel()

	sink(devcontainer.ProvisionEvent{
		Phase:  devcontainer.ProvisionPhase,
		Step:   devcontainer.ProvStepImageResolved,
		Status: devcontainer.ProvStatusCompleted,
		Tag:    "ghcr.io/crewship-ai/agent-runtime:latest",
		Digest: testImageDigest,
		Pinned: true,
	})
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	payload, traceID := readProvisionStepRow(t, h, crewID)
	if got := payload["digest"]; got != testImageDigest {
		t.Errorf("payload[digest] = %v, want %q — a cancelled run must still record its image", got, testImageDigest)
	}
	if traceID != runID {
		t.Errorf("trace_id = %q, want %q", traceID, runID)
	}
}
