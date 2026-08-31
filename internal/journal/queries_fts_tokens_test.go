package journal

// Tokenised free-text search (#2206). The original implementation
// wrapped the whole input in one FTS5 phrase literal, which made every
// multi-word search order-sensitive and every partial word unmatchable —
// on the running instance `morgan session` found 56 rows and
// `session morgan` found 0. These tests pin the tokenised behaviour:
// terms AND together, the last one prefix-matches, and every token is
// still inert as FTS5 syntax.
//
// Lives beside queries_fts_test.go and reuses its openTestDBWithFTS
// fixture so the FTS5 shadow table exists.

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedFTS emits the given summaries and waits for the writer flush so
// the FTS shadow table is populated before the assertions run.
func seedFTS(t *testing.T, db *sql.DB, summaries ...string) {
	t.Helper()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	for _, s := range summaries {
		_, _ = w.Emit(ctx, Entry{
			WorkspaceID: "ws_test",
			Type:        EntryPeerConversation,
			ActorType:   ActorAgent,
			Summary:     s,
		})
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)
}

// countFTS is a thin Count wrapper that fails the test on error.
func countFTS(t *testing.T, db *sql.DB, query string) int64 {
	t.Helper()
	n, err := Count(context.Background(), db, Query{WorkspaceID: "ws_test", FTSQuery: query})
	if err != nil {
		t.Fatalf("Count(FTSQuery=%q): %v", query, err)
	}
	return n
}

// WordOrderIndependent is the headline symptom of #2206: reverse two
// words and the result set must not change.
func TestList_FTSQuery_WordOrderIndependent(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	seedFTS(t, db,
		"morgan opened a session",
		"session closed by morgan",
		"unrelated line",
	)

	forward := countFTS(t, db, "morgan session")
	reverse := countFTS(t, db, "session morgan")
	if forward != 2 || reverse != 2 {
		t.Errorf("word order changed the result set: %q=%d %q=%d, want 2 and 2",
			"morgan session", forward, "session morgan", reverse)
	}
}

// PrefixMatchesLastToken: typing four letters of a word that occurs a
// thousand times must not return zero. Only the trailing token gets the
// prefix — the earlier ones are complete words the user already typed.
func TestList_FTSQuery_PrefixMatchesLastToken(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	seedFTS(t, db,
		"morgan restarted the session",
		"session for someone else",
	)

	if n := countFTS(t, db, "morg"); n != 1 {
		t.Errorf("prefix search %q = %d, want 1", "morg", n)
	}
	if n := countFTS(t, db, "session morg"); n != 1 {
		t.Errorf("prefix on last token %q = %d, want 1", "session morg", n)
	}
	// Leading tokens are NOT prefixes — "sess" is not a word in either row.
	if n := countFTS(t, db, "sess morgan"); n != 0 {
		t.Errorf("leading token must match whole words only; %q = %d, want 0", "sess morgan", n)
	}
}

// OperatorsStayLiteral is the safety guarantee the phrase quoting bought
// us, and it has to survive tokenisation: every token, operator-shaped
// or not, stays a literal term that has to be present.
func TestList_FTSQuery_OperatorsStayLiteral(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	seedFTS(t, db,
		"alpha one",
		"beta two",
		"gamma three",
	)

	cases := []struct {
		query string
		want  int64
	}{
		// If OR leaked through as an operator this would be 2.
		{"alpha OR beta", 0},
		// If NOT leaked through this would be 2 (everything but alpha).
		{"NOT alpha", 0},
		// NEAR() unquoted is either a syntax error or a proximity match.
		{"NEAR(alpha beta)", 0},
		// A bare AND between real terms still requires the literal "and".
		{"alpha AND one", 0},
		// A column filter must not reach the FTS5 parser as one.
		{"summary:alpha", 0},
		// The tokens on their own still work.
		{"alpha one", 1},
	}
	for _, tc := range cases {
		if n := countFTS(t, db, tc.query); n != tc.want {
			t.Errorf("FTSQuery=%q = %d, want %d (operator leaked into the query)", tc.query, n, tc.want)
		}
	}
}

// PunctuationOnlyTokensAreDropped: a lone `*` or `--` produces no FTS5
// token, and an empty phrase ANDed into the expression matches nothing.
// Dropping such tokens keeps `morgan *` from being a false empty result.
func TestList_FTSQuery_PunctuationOnlyTokensAreDropped(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	seedFTS(t, db, "morgan opened a session")

	for _, q := range []string{"morgan *", "* morgan", "morgan --", "morgan ***"} {
		if n := countFTS(t, db, q); n != 1 {
			t.Errorf("FTSQuery=%q = %d, want 1", q, n)
		}
	}
	// Input made up entirely of such tokens stays unmatchable rather than
	// widening to "everything".
	for _, q := range []string{"*", "***", "--"} {
		if n := countFTS(t, db, q); n != 0 {
			t.Errorf("FTSQuery=%q = %d, want 0", q, n)
		}
	}
}

// PayloadStillMatches guards the JOIN: tokenisation must not change
// which columns are searched. `q` covers summary *and* payload.
func TestList_FTSQuery_TokenisedMatchOnPayload(t *testing.T) {
	db := openTestDBWithFTS(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()
	ctx := context.Background()
	_, _ = w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		Type:        EntryExecCommand,
		ActorType:   ActorAgent,
		Summary:     "container died",
		Payload:     map[string]any{"error": "OOMKilled exit 137"},
	})
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	// One token from the summary, one from the payload, reversed.
	if n := countFTS(t, db, "OOMKilled container"); n != 1 {
		t.Errorf("cross-column AND = %d, want 1", n)
	}
}
