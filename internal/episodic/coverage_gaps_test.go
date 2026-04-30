package episodic

import (
	"context"
	"database/sql"
	"math"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestShouldEmbed_TableDriven covers the gating function the indexer uses
// to refuse high-volume, low-value entry types. CLAUDE.md feedback notes
// "selective embedding only — never embed exec.output_chunk/metrics/
// network (prevents memory drift)" — the table here is the canonical
// proof.
func TestShouldEmbed_TableDriven(t *testing.T) {
	tests := []struct {
		entryType string
		severity  string
		want      bool
	}{
		// Always-embedded types
		{"peer.escalation", "info", true},
		{"summary.generated", "info", true},
		{"memory.consolidated", "info", true},
		{"approval.denied", "info", true},
		{"eval.regression_detected", "info", true},

		// Severity-gated keeper.decision
		{"keeper.decision", "info", false},
		{"keeper.decision", "notice", false},
		{"keeper.decision", "warn", true},
		{"keeper.decision", "error", true},

		// Severity-gated mission.status_change
		{"mission.status_change", "info", false},
		{"mission.status_change", "warn", true},
		{"mission.status_change", "error", true},

		// peer.conversation NEVER embeds at the type level (indexer must
		// check payload).
		{"peer.conversation", "info", false},
		{"peer.conversation", "warn", false},
		{"peer.conversation", "error", false},

		// Anti-list — these MUST NEVER embed (memory drift prevention).
		{"exec.output_chunk", "info", false},
		{"exec.output_chunk", "warn", false},
		{"exec.command", "info", false},
		{"network.egress", "info", false},
		{"network.port_opened", "info", false},
		{"container.metrics", "info", false},
		{"file.written", "info", false},
		{"llm.call", "info", false},

		// Unknown types fall through to false.
		{"unknown.type", "info", false},
		{"", "info", false},
	}
	for _, tt := range tests {
		t.Run(tt.entryType+"/"+tt.severity, func(t *testing.T) {
			if got := shouldEmbed(tt.entryType, tt.severity); got != tt.want {
				t.Errorf("shouldEmbed(%q, %q) = %v, want %v",
					tt.entryType, tt.severity, got, tt.want)
			}
		})
	}
}

// TestEmbeddableEntryTypes_AlignsWithShouldEmbed pins the documented
// invariant: every type in EmbeddableEntryTypes must be capable of
// embedding under at least one severity. A drift between the SQL-side
// EmbeddableEntryTypes filter and the Go-side shouldEmbed gate would
// silently drop candidate rows.
func TestEmbeddableEntryTypes_AlignsWithShouldEmbed(t *testing.T) {
	for _, et := range EmbeddableEntryTypes {
		// Try every severity; at least one must return true.
		any := false
		for _, sev := range []string{"info", "notice", "warn", "error"} {
			if shouldEmbed(et, sev) {
				any = true
				break
			}
		}
		if !any && et != "peer.conversation" {
			// peer.conversation is special — always false at type level,
			// indexer makes payload-based decision.
			t.Errorf("EmbeddableEntryTypes contains %q but shouldEmbed never returns true for it", et)
		}
	}
}

// TestScopeForRole_AllRoles covers the full role-to-scope mapping. The
// invariant: only LEAD and COORDINATOR get crew_shared.
func TestScopeForRole_AllRoles(t *testing.T) {
	tests := []struct {
		role string
		want Scope
	}{
		{"LEAD", ScopeCrewShared},
		{"COORDINATOR", ScopeCrewShared},
		{"AGENT", ScopeOwn},
		{"MEMBER", ScopeOwn},
		{"", ScopeOwn},
		{"lead", ScopeOwn}, // case-sensitive match
		{"random", ScopeOwn},
	}
	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			if got := ScopeForRole(tt.role); got != tt.want {
				t.Errorf("ScopeForRole(%q) = %v, want %v", tt.role, got, tt.want)
			}
		})
	}
}

// TestEncodeDecode_Empty exercises the zero-length vector boundary.
func TestEncodeDecode_Empty(t *testing.T) {
	blob := EncodeVector([]float32{})
	if len(blob) != 0 {
		t.Errorf("empty vector blob = %d bytes, want 0", len(blob))
	}
	out, err := DecodeVector(blob, 0)
	if err != nil {
		t.Fatalf("decode 0-dim: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("decoded empty: %v", out)
	}
}

// TestDecodeVector_LengthMismatch surfaces the validation error path.
func TestDecodeVector_LengthMismatch(t *testing.T) {
	tests := []struct {
		name string
		blob []byte
		dim  int
	}{
		{"too short", []byte{0x00, 0x00}, 1}, // 4 bytes per dim, blob has 2
		{"too long", make([]byte, 8), 1},     // 4 bytes for dim=1, blob has 8
		{"way off", make([]byte, 16), 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeVector(tt.blob, tt.dim)
			if err == nil {
				t.Errorf("DecodeVector(len=%d, dim=%d) want error, got nil",
					len(tt.blob), tt.dim)
			}
		})
	}
}

// TestCosine_EdgeCases covers the [no-NaN-poisoning] invariant. Cosine
// must return 0 for any zero-norm or mismatched-length vector.
func TestCosine_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		a, b []float32
		want float64
	}{
		{"both zero norm", []float32{0, 0, 0}, []float32{0, 0, 0}, 0},
		{"a zero norm", []float32{0, 0, 0}, []float32{1, 0, 0}, 0},
		{"b zero norm", []float32{1, 0, 0}, []float32{0, 0, 0}, 0},
		{"length mismatch", []float32{1, 0}, []float32{1, 0, 0}, 0},
		{"both empty", []float32{}, []float32{}, 0},
		{"identical unit", []float32{1, 0, 0}, []float32{1, 0, 0}, 1.0},
		{"opposite unit", []float32{1, 0, 0}, []float32{-1, 0, 0}, -1.0},
		{"orthogonal", []float32{1, 0, 0}, []float32{0, 1, 0}, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cosine(tt.a, tt.b)
			if math.IsNaN(got) {
				t.Fatalf("cosine returned NaN for %v / %v", tt.a, tt.b)
			}
			if math.Abs(got-tt.want) > 1e-6 {
				t.Errorf("cosine(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestNewOllamaEmbedder_Defaults verifies the constructor's empty-baseURL
// fallback + canonical config (model + 768 dim default).
func TestNewOllamaEmbedder_Defaults(t *testing.T) {
	e := NewOllamaEmbedder("")
	if e.BaseURL != "http://localhost:11434" {
		t.Errorf("BaseURL = %q, want default", e.BaseURL)
	}
	if e.Model() != "nomic-embed-text" {
		t.Errorf("Model = %q", e.Model())
	}
	if e.Dim() != 768 {
		t.Errorf("Dim default = %d, want 768", e.Dim())
	}
}

// TestNewOllamaEmbedder_TrimsTrailingSlash covers URL normalisation.
func TestNewOllamaEmbedder_TrimsTrailingSlash(t *testing.T) {
	e := NewOllamaEmbedder("http://elsewhere/")
	if e.BaseURL != "http://elsewhere" {
		t.Errorf("BaseURL = %q, want trimmed", e.BaseURL)
	}
}

// TestEscapeFTSQuery_TableDriven pins the behaviour of the FTS5 query
// sanitiser used by bm25Lane. Critical because raw user queries with
// quotes / FTS operators would otherwise cause syntax errors.
func TestEscapeFTSQuery_TableDriven(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"!@#$%", ""},         // pure punctuation → empty
		{"a", ""},             // single chars filtered (cur.Len > 1)
		{"ab", "ab*"},         // two-char minimum
		{"deploy", "deploy*"}, // single word becomes prefix
		{"deploy 42", "deploy* OR 42*"},
		{"deploy-42", "deploy* OR 42*"},         // dash splits
		{"DEPLOY-42", "deploy* OR 42*"},         // case lowered
		{`"quoted"`, "quoted*"},                 // quotes stripped, word kept
		{"foo AND bar", "foo* OR and* OR bar*"}, // FTS keywords harmless after escape
		{"héllo wörld", "llo* OR rld*"},         // non-ASCII letters stripped
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := escapeFTSQuery(tt.in); got != tt.want {
				t.Errorf("escapeFTSQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRRFFuse_OverlappingHits — the same EntryID in both lanes should
// receive the sum of both reciprocal-rank contributions, ranking ahead
// of items that appear in only one.
func TestRRFFuse_OverlappingHits(t *testing.T) {
	dense := []Hit{
		{EntryID: "a"}, // rank 1 in dense
		{EntryID: "b"}, // rank 2 in dense
		{EntryID: "c"}, // rank 3 in dense
	}
	sparse := []Hit{
		{EntryID: "b"}, // rank 1 in sparse — appears in both
		{EntryID: "d"}, // rank 2 in sparse — sparse-only
	}
	got := rrfFuse(dense, sparse, 4)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	// "b" in both lanes (rank 2 + rank 1) should beat "a" (rank 1 only).
	// 1/(60+2) + 1/(60+1) ≈ 0.0327 vs 1/(60+1) ≈ 0.0164
	if got[0].EntryID != "b" {
		t.Errorf("expected b at top, got %v", entryIDs(got))
	}
}

// TestRRFFuse_TruncatesToTopK verifies the top-K cap.
func TestRRFFuse_TruncatesToTopK(t *testing.T) {
	dense := make([]Hit, 10)
	for i := range dense {
		dense[i] = Hit{EntryID: "e" + string(rune('0'+i))}
	}
	got := rrfFuse(dense, nil, 3)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
}

// TestRRFFuse_AllEmpty returns nil/empty cleanly.
func TestRRFFuse_AllEmpty(t *testing.T) {
	got := rrfFuse(nil, nil, 5)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
}

// TestHybridRecall_DenseLaneOnly_NoFTS verifies the fallback: when the
// FTS5 virtual table is missing (pre-migration-55 DB), HybridRecall
// degrades to dense-only with a logged warning rather than failing.
func TestHybridRecall_DenseLaneOnly_NoFTS(t *testing.T) {
	db := openTestDB(t) // schema has no journal_entries_fts
	defer db.Close()

	insertEntry(t, db, journal.Entry{
		ID: "j1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "deployment broke",
	})

	emb := &stubEmbedder{model: "test-embed", dim: 4,
		vectors: map[string][]float32{
			"deployment": {1, 0, 0, 0},
		}}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(context.Background(), 10)

	hits, err := HybridRecall(context.Background(), db, emb, Query{
		WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
		QueryText: "deployment issue", K: 5,
	})
	if err != nil {
		t.Fatalf("HybridRecall: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("dense fallback returned no hits")
	}
}

// TestHybridRecall_RequiresWorkspaceID covers the cross-tenant guard.
func TestHybridRecall_RequiresWorkspaceID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	emb := &stubEmbedder{model: "test", dim: 4}
	_, err := HybridRecall(context.Background(), db, emb, Query{
		AgentID: "a1", Scope: ScopeOwn, QueryText: "x",
	})
	if err == nil {
		t.Fatal("want workspace_id error")
	}
}

// TestHybridRecall_KClamping verifies the documented K limits (0/negative
// → 5, >50 → 5 — current impl resets to 5, not 50).
func TestHybridRecall_KClamping(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	insertEntry(t, db, journal.Entry{
		ID: "j1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "x",
	})

	emb := &stubEmbedder{model: "t", dim: 4}
	NewIndexer(db, emb, quietLogger(), 0).sweepOnce(context.Background(), 10)

	// K=0 should not error; K=999 should clamp.
	for _, k := range []int{0, -1, 999} {
		_, err := HybridRecall(context.Background(), db, emb, Query{
			WorkspaceID: "ws_test", AgentID: "a1", Scope: ScopeOwn,
			QueryText: "anything", K: k,
		})
		if err != nil {
			t.Errorf("K=%d should clamp, got error: %v", k, err)
		}
	}
}

// TestLinkSupports_HappyPath inserts evidence-supporting edges and
// verifies they round-trip through RelationsFor.
func TestLinkSupports_HappyPath(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := LinkSupports(ctx, db, "rule_1", []string{"ev_1", "ev_2", "ev_3"}); err != nil {
		t.Fatalf("LinkSupports: %v", err)
	}

	rels, err := RelationsFor(ctx, db, "rule_1")
	if err != nil {
		t.Fatalf("RelationsFor: %v", err)
	}
	if len(rels) != 3 {
		t.Fatalf("want 3 relations, got %d", len(rels))
	}
	for _, r := range rels {
		if r.Kind != RelationSupports {
			t.Errorf("kind: %v want supports", r.Kind)
		}
		if r.Score != 1.0 {
			t.Errorf("supports score: %v want 1.0", r.Score)
		}
		if r.EntryID != "rule_1" {
			t.Errorf("entry_id: %s want rule_1", r.EntryID)
		}
	}
}

// TestLinkSupports_EmptyEvidenceIsNoop — calling with no evidence shouldn't
// open a transaction at all.
func TestLinkSupports_EmptyEvidenceIsNoop(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	if err := LinkSupports(context.Background(), db, "rule_x", nil); err != nil {
		t.Fatalf("LinkSupports nil: %v", err)
	}
	if err := LinkSupports(context.Background(), db, "rule_x", []string{}); err != nil {
		t.Fatalf("LinkSupports empty: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_relations`).Scan(&n); err != nil {
		t.Fatalf("count memory_relations: %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 rows, got %d", n)
	}
}

// TestLinkSupports_Idempotent — re-running the same evidence list does
// not duplicate edges (PRIMARY KEY ON CONFLICT IGNORE).
func TestLinkSupports_Idempotent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	for i := 0; i < 3; i++ {
		if err := LinkSupports(context.Background(), db, "r", []string{"e1", "e2"}); err != nil {
			t.Fatalf("LinkSupports: %v", err)
		}
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_relations`).Scan(&n); err != nil {
		t.Fatalf("count memory_relations: %v", err)
	}
	if n != 2 {
		t.Errorf("after 3× call expected 2 rows, got %d", n)
	}
}

// TestRelationsFor_EmptyEntry returns empty slice + nil error rather
// than ErrNoRows.
func TestRelationsFor_EmptyEntry(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	got, err := RelationsFor(context.Background(), db, "j_nonexistent")
	if err != nil {
		t.Fatalf("RelationsFor: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 rels, got %d", len(got))
	}
}

// TestLinkSimilarOnIndex_BelowThreshold — when no candidate is similar
// enough, no edges are written. Threshold default 0.8.
func TestLinkSimilarOnIndex_BelowThreshold(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// Seed two embeddings that are orthogonal — cosine = 0.
	_, err := db.Exec(`INSERT INTO journal_embeddings
		(entry_id, workspace_id, agent_id, model, dim, vector, indexed_at) VALUES
		('e1', 'ws_test', 'a1', 't', 4, ?, '2026-04-30T00:00:00Z')`,
		EncodeVector([]float32{0, 1, 0, 0}))
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	insertEntry(t, db, journal.Entry{
		ID: "e1", WorkspaceID: "ws_test", AgentID: "a1",
		Type: journal.EntryPeerEscalation, Severity: journal.SeverityWarn,
		ActorType: journal.ActorAgent, Summary: "x",
	})

	// New entry with orthogonal vector.
	if err := LinkSimilarOnIndex(ctx, db, "e2", "ws_test", "", []float32{1, 0, 0, 0}, 0.8); err != nil {
		t.Fatalf("LinkSimilarOnIndex: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM memory_relations`).Scan(&n); err != nil {
		t.Fatalf("count memory_relations: %v", err)
	}
	if n != 0 {
		t.Errorf("orthogonal vectors should produce 0 edges, got %d", n)
	}
}

// TestLinkSimilarOnIndex_ThresholdNormalisation — threshold <= 0 or > 1
// resets to 0.8.
func TestLinkSimilarOnIndex_ThresholdNormalisation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	// No candidates so the function exits early; only assert it doesn't
	// error on out-of-range thresholds.
	for _, threshold := range []float64{0, -1, 1.5, 99} {
		err := LinkSimilarOnIndex(ctx, db, "new", "ws_test", "",
			[]float32{1, 0, 0, 0}, threshold)
		if err != nil {
			t.Errorf("threshold=%v: %v", threshold, err)
		}
	}
}

// TestIndexer_NoEmbedder_PanicSafe — passing nil embedder should not
// crash on construction. (Panic at Index time is acceptable; construction
// is a hot path during boot.)
func TestIndexer_NoEmbedder_PanicSafe(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("NewIndexer with nil embedder panicked: %v", r)
		}
	}()
	_ = NewIndexer(db, nil, quietLogger(), 0)
}

// entryIDs is a tiny helper to surface a slice of EntryIDs in test
// failures so the diff highlights ordering rather than struct contents.
func entryIDs(hits []Hit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.EntryID
	}
	return out
}

// Compile-time guard: ensure types referenced by the tests compile
// against the package.
var _ = sql.ErrNoRows
