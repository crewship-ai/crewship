package episodic

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// indexer.go — qualifies (the selective-embedding filter) + IndexOne
// short-circuit + Start cancellation contract.
//
// qualifies is the gate that prevents the high-volume low-signal corpus
// (exec.output_chunk, container.metrics, etc.) from polluting episodic
// recall. The contract is documented in the source comment:
//   - shouldEmbed-true types always qualify
//   - types NOT in EmbeddableEntryTypes can never be force-indexed,
//     even via refs.episodic=true (the "bounded override" rule)
//   - peer.conversation needs either refs.episodic=true OR
//     payload.state=="escalated"
//   - other allowlisted-but-severity-gated types accept refs.episodic
//     as an upgrade
// ---------------------------------------------------------------------------

func newIndexerForTest(t *testing.T) *Indexer {
	t.Helper()
	return NewIndexer(nil, &stubEmbedder{model: "t", dim: 4}, slog.Default(), time.Second)
}

func TestIndexerQualifies_ShouldEmbedTypesAlwaysQualify(t *testing.T) {
	// shouldEmbed-true types (peer.escalation, summary.generated, etc.)
	// always pass qualifies without any refs / payload signal.
	x := newIndexerForTest(t)
	for _, et := range []journal.EntryType{
		journal.EntryType("peer.escalation"),
		journal.EntryType("summary.generated"),
		journal.EntryType("memory.consolidated"),
		journal.EntryType("approval.denied"),
		journal.EntryType("eval.regression_detected"),
	} {
		t.Run(string(et), func(t *testing.T) {
			if !x.qualifies(journal.Entry{Type: et}) {
				t.Errorf("qualifies(%q) = false; should always be true (shouldEmbed yes)", et)
			}
		})
	}
}

func TestIndexerQualifies_SeverityGatedTypes(t *testing.T) {
	// keeper.decision + mission.status_change only qualify when
	// severity is warn or error. info-level instances of these types
	// stay out of episodic memory.
	x := newIndexerForTest(t)
	cases := []struct {
		name  string
		entry journal.Entry
		want  bool
	}{
		{"keeper-warn", journal.Entry{Type: "keeper.decision", Severity: journal.SeverityWarn}, true},
		{"keeper-error", journal.Entry{Type: "keeper.decision", Severity: journal.SeverityError}, true},
		{"keeper-info-not-qualified", journal.Entry{Type: "keeper.decision", Severity: journal.SeverityInfo}, false},
		{"mission-warn", journal.Entry{Type: "mission.status_change", Severity: journal.SeverityWarn}, true},
		{"mission-info-not-qualified", journal.Entry{Type: "mission.status_change", Severity: journal.SeverityInfo}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := x.qualifies(tc.entry); got != tc.want {
				t.Errorf("qualifies(%+v) = %v, want %v", tc.entry, got, tc.want)
			}
		})
	}
}

func TestIndexerQualifies_ForbiddenTypes_NeverQualify_EvenWithOverride(t *testing.T) {
	// The "bounded override" rule from the source comment:
	// refs.episodic=true CANNOT force-index types outside
	// EmbeddableEntryTypes (exec.output_chunk, container.metrics,
	// network.*, file.*). Pin the security boundary so a regression
	// that started honouring refs.episodic for any type would surface
	// here AND silently let high-volume noise into recall.
	x := newIndexerForTest(t)
	for _, et := range []journal.EntryType{
		"exec.output_chunk",
		"container.metrics",
		"network.tcp_open",
		"file.write",
		"random.invented.type",
	} {
		t.Run(string(et), func(t *testing.T) {
			// Even with the override flag, forbidden types must NOT qualify.
			if x.qualifies(journal.Entry{
				Type: et,
				Refs: map[string]any{"episodic": true},
			}) {
				t.Errorf("qualifies(%q) = true with refs.episodic=true; forbidden type must stay forbidden", et)
			}
			// And without the override, of course also false.
			if x.qualifies(journal.Entry{Type: et}) {
				t.Errorf("qualifies(%q) = true with no signals; forbidden type must stay forbidden", et)
			}
		})
	}
}

func TestIndexerQualifies_PeerConversation_ByEscalatedState(t *testing.T) {
	// Source: peer.conversation only qualifies when its payload.state
	// is "escalated" (the rationale being plain Q&A would dilute
	// recall). Pin both the qualifying and non-qualifying state.
	x := newIndexerForTest(t)

	if !x.qualifies(journal.Entry{
		Type:    journal.EntryPeerConversation,
		Payload: map[string]any{"state": "escalated"},
	}) {
		t.Error("peer.conversation with state=escalated should qualify")
	}
	if x.qualifies(journal.Entry{
		Type:    journal.EntryPeerConversation,
		Payload: map[string]any{"state": "answered"},
	}) {
		t.Error("peer.conversation with state=answered must NOT qualify (would dilute recall)")
	}
	if x.qualifies(journal.Entry{
		Type:    journal.EntryPeerConversation,
		Payload: map[string]any{"state": "open"},
	}) {
		t.Error("peer.conversation with state=open must NOT qualify")
	}
	// Missing payload.state at all → not qualified.
	if x.qualifies(journal.Entry{Type: journal.EntryPeerConversation}) {
		t.Error("peer.conversation with no payload.state must NOT qualify")
	}
}

func TestIndexerQualifies_PeerConversation_ByEpisodicRefsFlag(t *testing.T) {
	// refs.episodic=true is the manual upgrade override — qualifies
	// peer.conversation even without payload.state=escalated.
	x := newIndexerForTest(t)
	if !x.qualifies(journal.Entry{
		Type: journal.EntryPeerConversation,
		Refs: map[string]any{"episodic": true},
	}) {
		t.Error("peer.conversation with refs.episodic=true should qualify (manual override)")
	}
	// refs.episodic=false (explicit non-override) → not qualified
	// for peer.conversation in plain state.
	if x.qualifies(journal.Entry{
		Type: journal.EntryPeerConversation,
		Refs: map[string]any{"episodic": false},
	}) {
		t.Error("peer.conversation with refs.episodic=false must NOT qualify")
	}
}

func TestIndexerQualifies_SeverityGatedType_UpgradedByEpisodicRefs(t *testing.T) {
	// Source comment: "refs.episodic=true as upgrade for
	// allowlisted-but-severity-gated types (e.g., an info-level
	// keeper.decision an operator explicitly wants remembered)."
	x := newIndexerForTest(t)
	if !x.qualifies(journal.Entry{
		Type:     "keeper.decision",
		Severity: journal.SeverityInfo, // would normally NOT qualify
		Refs:     map[string]any{"episodic": true},
	}) {
		t.Error("info-level keeper.decision with refs.episodic=true should qualify (override)")
	}
	// Without the refs override, an info-level keeper.decision must
	// still NOT qualify (regression-pin for the severity gate).
	if x.qualifies(journal.Entry{
		Type:     "keeper.decision",
		Severity: journal.SeverityInfo,
	}) {
		t.Error("info-level keeper.decision without refs override must NOT qualify")
	}
}

// ---- IndexOne ----

// countingEmbedder wraps Embedder with a call counter so the
// IndexOne short-circuit test can assert no embed call fires.
type countingEmbedder struct {
	calls int
}

func (c *countingEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	c.calls++
	return []float32{0, 0, 0, 0}, nil
}
func (c *countingEmbedder) Dim() int      { return 4 }
func (c *countingEmbedder) Model() string { return "counting" }

func TestIndexOne_DoesNotQualify_ReturnsNilWithoutEmbedderCall(t *testing.T) {
	// IndexOne's short-circuit: if qualifies-false, return nil silently
	// (callers can call unconditionally). Pin that no embedder call
	// fires — a regression that always embedded would burn Ollama
	// quota on the high-volume forbidden corpus.
	embedder := &countingEmbedder{}
	x := NewIndexer(nil, embedder, slog.Default(), time.Second)

	// Use a forbidden type — guaranteed not to qualify.
	err := x.IndexOne(context.Background(), journal.Entry{
		Type:    "exec.output_chunk",
		Summary: "should not embed",
	})
	if err != nil {
		t.Errorf("IndexOne on non-qualifying entry = %v, want nil", err)
	}
	if embedder.calls != 0 {
		t.Errorf("embedder called %d times for non-qualifying entry; want 0", embedder.calls)
	}
}

// (Start's cancellation contract is left to integration tests that
// own a real *sql.DB — Start's initial sweepOnce dereferences x.db,
// so we can't exercise the loop without a migrated schema. The
// sweepOnce, IndexOne, and qualifies coverage above is enough to
// pin the selective-filter contract.)

// stubEmbedder is reused from episodic_test.go for the qualifies-only
// tests where the embedder is wired but never invoked.
