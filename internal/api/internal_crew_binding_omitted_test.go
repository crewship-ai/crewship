package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// #1222 / #1186 residual — "own crew, or no crew".
//
// #1202 closed the misattribution half: a crew-bound (crwv1) token can no
// longer name a *sibling* crew. But assertBoundCrewWorkspaceDB skips empty
// IDs by design ("optional fields"), and requireInternal deliberately does
// not inject crew_id the way it injects workspace_id. So a crew-bound token
// could still simply *omit* the crew field and land a cost row / journal
// entry / MCP-tool-call audit row / saved pipeline with NULL crew
// attribution — the binding guarantee held for "which crew", but not for
// "a crew at all".
//
// The fix is the issue's option (b): auto-inject the token's bound crew
// when the field is omitted, mirroring requireInternal's workspace
// injection. Option (a) — 400 on empty — was not taken: these fields are
// legitimately optional for workspace-bound (wsv1) and master callers, and
// rejecting would break them for no security gain.
func TestBindOmittedCrew(t *testing.T) {
	t.Parallel()

	const boundCrew = "crew-bound-123"
	const otherCrew = "crew-other-456"

	cases := []struct {
		name string
		// ctxCrew is the crew the token is bound to ("" = wsv1/master).
		ctxCrew string
		// in is the crew_id the caller supplied in the body.
		in   string
		want string
	}{
		{
			// The bug: omitted + crew-bound → attributed to the token's
			// own crew rather than to nothing.
			name:    "crew-bound token omitting the crew gets its own crew",
			ctxCrew: boundCrew,
			in:      "",
			want:    boundCrew,
		},
		{
			// Whitespace must not be a bypass for the above.
			name:    "crew-bound token sending blank crew gets its own crew",
			ctxCrew: boundCrew,
			in:      "   ",
			want:    boundCrew,
		},
		{
			// Injection must never override an explicit value — that stays
			// assertBoundCrewWorkspaceDB's job, which 403s a sibling.
			// Silently rewriting it here would turn a 403 into a success.
			name:    "explicit crew is passed through untouched",
			ctxCrew: boundCrew,
			in:      otherCrew,
			want:    otherCrew,
		},
		{
			// wsv1 / master callers have no binding: these endpoints stay
			// legitimately workspace-wide for them. This is the
			// regression guard for the non-crew paths.
			name:    "workspace-bound token is unaffected",
			ctxCrew: "",
			in:      "",
			want:    "",
		},
		{
			name:    "workspace-bound token with explicit crew unaffected",
			ctxCrew: "",
			in:      otherCrew,
			want:    otherCrew,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if tc.ctxCrew != "" {
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, tc.ctxCrew)
			}
			if got := bindOmittedCrew(ctx, tc.in); got != tc.want {
				t.Errorf("bindOmittedCrew(ctx[crew=%q], %q) = %q, want %q",
					tc.ctxCrew, tc.in, got, tc.want)
			}
		})
	}
}

// TestSidecarCostRecord_CrewBoundToken_OmittedCrewAttributed drives the
// real handler: a crew-bound token that omits crew_id must not be able to
// write an unattributed ledger row.
func TestSidecarCostRecord_CrewBoundToken_OmittedCrewAttributed(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "crew-cost-bind", wsID, "Bound Crew", "bound-crew")

	r := &Router{db: db, logger: newTestLogger()}

	body := `{"workspace_id":"` + wsID + `","provider":"anthropic","model":"claude-opus-4-8",` +
		`"input_tokens":10,"output_tokens":5}`
	req := httptest.NewRequest("POST", "/api/v1/internal/cost/record", strings.NewReader(body))
	ctx := context.WithValue(req.Context(), ctxInternalTokenWS, wsID)
	ctx = context.WithValue(ctx, ctxInternalTokenCrew, crewID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	r.handleSidecarCostRecord(rr, req)

	if rr.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}

	// The row must carry the token's crew, not NULL.
	var gotCrew *string
	if err := db.QueryRow(
		`SELECT crew_id FROM cost_ledger WHERE workspace_id = ? ORDER BY rowid DESC LIMIT 1`,
		wsID,
	).Scan(&gotCrew); err != nil {
		t.Fatalf("read ledger row: %v", err)
	}
	if gotCrew == nil {
		t.Fatal("ledger row has NULL crew_id — a crew-bound token wrote an unattributed row (#1222)")
	}
	if *gotCrew != crewID {
		t.Errorf("ledger crew_id = %q, want %q (the token's bound crew)", *gotCrew, crewID)
	}
}
