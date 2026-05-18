package api

// Diff-endpoint coverage for the HITL memory proposal flow.
//
// All HTTP-contract scenarios share enough setup/assert
// scaffolding to fit into one table-driven test (per the
// repo-wide "**/*_test.go: table-driven + t.Run" guideline
// CodeRabbit cited on PR #409). The byte-equality "no drift
// between preview and Approve" case is the one exception —
// it actually invokes ApproveProposal and reads the canonical
// file off disk, which doesn't pivot cleanly into the same
// fixture shape, so it stays a standalone test below.
//
// Contracts pinned here:
//
//   1. GET /diff returns 200 with a unified diff that, when
//      conceptually "applied", produces what an Approve on
//      the same proposal would write to the canonical
//      learned-*.md file.
//   2. Cross-workspace probes get 404, not 403 (no existence
//      leak).
//   3. Missing markdown on disk surfaces as 410 Gone (distinct
//      from 404 — the row exists but the artefact is gone,
//      recoverable via re-running the consolidator).
//   4. First-time-canonical and append-case produce different
//      diff prefixes; both verified.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/consolidate"
	"github.com/crewship-ai/crewship/internal/journal"
)

// diffDecode unmarshals a successful response body. Caller
// checks rr.Code first; for non-2xx cases this is unused.
func diffDecode(t *testing.T, rr *httptest.ResponseRecorder) proposalDiffResponse {
	t.Helper()
	var resp proposalDiffResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode diff response: %v; body=%s", err, rr.Body.String())
	}
	return resp
}

// diffTestCtx carries the per-case fixture that prep() builds
// and that the assertions use. h/db/userID/wsID/crewID come
// from newProposedHandlerTest; proposalID/proposalPath come
// from seedProposalRow (or are zero-valued for the "no
// proposal" case). `now` is captured BEFORE the handler call
// — Rabbit-flagged flake: time.Now().UTC() AFTER the handler
// can cross a UTC-day boundary near midnight and make
// expectedCanonical differ from resp.CanonicalPath by one day.
type diffTestCtx struct {
	h            *ProposedHandler
	db           *sql.DB
	userID       string
	wsID         string
	crewID       string
	proposalID   string
	proposalPath string
	now          time.Time
}

func TestProposed_Diff_HTTPContract(t *testing.T) {
	cases := []struct {
		name string
		// authRole: empty string means "skip auth wiring entirely"
		// (the missing-workspace case). Non-empty means
		// withWorkspaceUser is applied with this role.
		authRole string
		// useDifferentWS: true switches the authed workspace to a
		// second tenant. The cross-workspace probe case.
		useDifferentWS bool
		// useEmptyPathID: true binds an empty "id" path value.
		useEmptyPathID bool
		// useUnknownID: true binds an unknown proposal id.
		useUnknownID bool
		// seedCanonical: true pre-creates a canonical file so the
		// append branch fires.
		seedCanonical bool
		// deleteMarkdown: true removes the proposal markdown after
		// seedProposalRow runs (the 410 Gone case).
		deleteMarkdown bool
		// expectedStatus: HTTP status code the handler must return.
		expectedStatus int
		// assertSuccessBody: optional callback for 200-path body
		// inspection. Skipped on non-2xx cases.
		assertSuccessBody func(t *testing.T, tc diffTestCtx, resp proposalDiffResponse)
	}{
		{
			name:           "happy_path_first_time_canonical",
			authRole:       "MEMBER",
			expectedStatus: http.StatusOK,
			assertSuccessBody: func(t *testing.T, tc diffTestCtx, resp proposalDiffResponse) {
				if resp.ProposalID != tc.proposalID || resp.WorkspaceID != tc.wsID || resp.CrewID != tc.crewID {
					t.Errorf("identity fields wrong: got %+v", resp)
				}
				if resp.Status != "pending" {
					t.Errorf("status = %q, want pending", resp.Status)
				}
				if resp.CanonicalExists {
					t.Errorf("canonical_exists = true on a brand-new fixture; want false")
				}
				if resp.ProposalPath != tc.proposalPath {
					t.Errorf("proposal_path = %q, want %q", resp.ProposalPath, tc.proposalPath)
				}
				// Use the pre-captured tc.now so a UTC-day rollover
				// between request and assertion can't flake the
				// test. The handler builds the canonical path from
				// time.Now().UTC() too, but at MOST a few μs apart
				// from tc.now since we captured immediately before
				// h.Diff.
				expectedCanonical := filepath.Join(filepath.Dir(filepath.Dir(tc.proposalPath)),
					"learned-"+tc.now.Format("2006-01-02")+".md")
				if resp.CanonicalPath != expectedCanonical {
					t.Errorf("canonical_path = %q, want %q", resp.CanonicalPath, expectedCanonical)
				}
				if resp.RulesCount != 1 || resp.Stats.RulesAppended != 1 {
					t.Errorf("rules_count = %d / rules_appended = %d; want 1/1",
						resp.RulesCount, resp.Stats.RulesAppended)
				}
				if resp.Stats.Additions <= 0 {
					t.Errorf("additions = %d; want >0", resp.Stats.Additions)
				}
				if resp.Stats.Deletions != 0 {
					t.Errorf("deletions = %d; want 0 (append-only merge)", resp.Stats.Deletions)
				}
				if !strings.Contains(resp.Diff, "--- canonical (current)") ||
					!strings.Contains(resp.Diff, "+++ canonical (post-merge)") {
					t.Errorf("diff missing unified header; got:\n%s", resp.Diff)
				}
				if !strings.Contains(resp.Diff, "Learned rules") {
					t.Errorf("first-time diff missing the file header; got:\n%s", resp.Diff)
				}
			},
		},
		{
			name:           "append_case_diff_shows_section_divider",
			authRole:       "MEMBER",
			seedCanonical:  true,
			expectedStatus: http.StatusOK,
			assertSuccessBody: func(t *testing.T, _ diffTestCtx, resp proposalDiffResponse) {
				if !resp.CanonicalExists {
					t.Errorf("canonical_exists = false; want true")
				}
				if resp.Stats.Deletions != 0 {
					t.Errorf("deletions = %d; want 0 in append branch", resp.Stats.Deletions)
				}
				if !strings.Contains(resp.Diff, "---\n") {
					t.Errorf("append diff missing the section divider; got:\n%s", resp.Diff)
				}
			},
		},
		{
			name:           "cross_workspace_probe_returns_404",
			authRole:       "OWNER",
			useDifferentWS: true,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "missing_proposal_markdown_returns_410",
			authRole:       "MEMBER",
			deleteMarkdown: true,
			expectedStatus: http.StatusGone,
		},
		{
			name: "missing_workspace_returns_401",
			// authRole="" → no withWorkspaceUser wiring. The
			// handler must 401 from the workspace check.
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "missing_proposal_id_returns_400",
			authRole:       "MEMBER",
			useEmptyPathID: true,
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "unknown_proposal_id_returns_404",
			authRole:       "MEMBER",
			useUnknownID:   true,
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID, crewID := newProposedHandlerTest(t)
			ctx := diffTestCtx{
				h: h, db: db, userID: userID, wsID: wsID, crewID: crewID,
			}

			// Per-case prep — only one proposal row by default;
			// the missing-WS / empty-id / unknown-id cases skip
			// it because they probe before the lookup happens.
			needsProposal := !(tc.useUnknownID || tc.authRole == "" || tc.useEmptyPathID)
			if needsProposal {
				ctx.proposalID, ctx.proposalPath = seedProposalRow(t, db, wsID, crewID, "pending")
			}
			if tc.deleteMarkdown {
				if err := os.Remove(ctx.proposalPath); err != nil {
					t.Fatalf("delete proposal markdown: %v", err)
				}
			}
			if tc.seedCanonical {
				canonicalPath := consolidate.CanonicalPathForProposal(ctx.proposalPath, time.Now().UTC())
				if err := os.WriteFile(canonicalPath, []byte("# Learned rules — pre-existing\n\nbody\n"), 0o644); err != nil {
					t.Fatalf("seed canonical: %v", err)
				}
			}

			// Build the request. Determine the path id to bind:
			// happy/append/cross/missing-md → real proposalID;
			// empty-id case binds ""; unknown-id case binds
			// a synthetic id; missing-WS case never gets here
			// because authRole=="" short-circuits the auth check
			// before path lookup even runs.
			pathID := ctx.proposalID
			urlID := ctx.proposalID
			if tc.useEmptyPathID {
				pathID = ""
				urlID = ""
			}
			if tc.useUnknownID {
				pathID = "mp_no_such"
				urlID = "mp_no_such"
			}
			req := httptest.NewRequest("GET", "/api/v1/consolidate/proposed/"+urlID+"/diff", nil)
			req.SetPathValue("id", pathID)

			switch {
			case tc.useDifferentWS:
				// Inline a second tenant — seedTestUser /
				// seedTestWorkspace use fixed IDs so they'd
				// UNIQUE-collide with the rig's first tenant.
				otherUserID, otherWS := seedSecondTenant(t, db)
				req = withWorkspaceUser(req, otherUserID, otherWS, tc.authRole)
			case tc.authRole != "":
				req = withWorkspaceUser(req, userID, wsID, tc.authRole)
			}

			// Capture now() immediately before the handler call
			// so any wall-clock-derived assertion (canonical path
			// date stamp) uses the same instant the handler does.
			ctx.now = time.Now().UTC()

			rr := httptest.NewRecorder()
			h.Diff(rr, req)

			if rr.Code != tc.expectedStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, tc.expectedStatus, rr.Body.String())
			}
			if rr.Code == http.StatusOK && tc.assertSuccessBody != nil {
				tc.assertSuccessBody(t, ctx, diffDecode(t, rr))
			}
		})
	}
}

// seedSecondTenant inserts a second user + workspace + member
// row with explicit unique IDs so the cross-workspace probe
// case can run alongside the rig's first tenant without
// UNIQUE-constraint collisions (seedTestUser / seedTestWorkspace
// in router_test.go use fixed IDs).
func seedSecondTenant(t *testing.T, db *sql.DB) (userID, workspaceID string) {
	t.Helper()
	userID = "test-other-user-id"
	workspaceID = "test-other-workspace-id"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'Other User')`, userID); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, workspaceID); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m_other', ?, ?, 'OWNER')`, workspaceID, userID); err != nil {
		t.Fatalf("insert other member: %v", err)
	}
	return userID, workspaceID
}

func TestProposed_Diff_ByteEqualToApprove_NoDriftBetweenPreviewAndWrite(t *testing.T) {
	// Load-bearing assertion: the post-merge half of the diff
	// must be byte-identical to what an Approve on the same
	// proposal would land on disk. Drift between preview and
	// write erodes operator trust in the HITL UI in the most
	// damaging way (silent disagreement).
	//
	// Strategy:
	//   1. Build the "post-merge" bytes the diff would render
	//      via the same helpers the handler uses.
	//   2. Approve the proposal — which writes the canonical
	//      file.
	//   3. Assert the file on disk equals the simulated bytes.
	//
	// Both branches capture `now` from time.Now() exactly once
	// before either call, so the date strings used to derive
	// the canonical filename match regardless of how close to
	// midnight the test runs. The "Approved at HH:MM:SS MST"
	// line CAN differ second-to-second, so we strip it before
	// comparison.
	_, db, userID, wsID, crewID := newProposedHandlerTest(t)
	proposalID, proposalPath := seedProposalRow(t, db, wsID, crewID, "pending")

	now := time.Now().UTC()
	canonicalPath := consolidate.CanonicalPathForProposal(proposalPath, now)
	rulesBody, err := os.ReadFile(proposalPath)
	if err != nil {
		t.Fatalf("read proposal body: %v", err)
	}
	previewBlock := consolidate.BuildCanonicalAppendBlock(false, now,
		consolidate.ExtractProposalRulesBody(string(rulesBody)))

	jw := journal.NewWriter(db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	_, err = consolidate.ApproveProposal(context.Background(), db, jw, newTestLogger(), proposalID, userID,
		consolidate.ApprovalOptions{})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}

	got, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatalf("read canonical after approve: %v", err)
	}

	want := stripApprovedAtLine(previewBlock)
	have := stripApprovedAtLine(string(got))
	if want != have {
		t.Fatalf("preview vs. approve byte mismatch (excluding 'Approved at' line)\n--want--\n%s\n--have--\n%s",
			want, have)
	}
}

// ── helpers ──────────────────────────────────────────────────────────

// stripApprovedAtLine removes the "## Approved at HH:MM:SS MST"
// line from a learned-*.md block so the byte-equality test can
// compare everything else without racing the wall clock.
func stripApprovedAtLine(s string) string {
	out := make([]string, 0, 16)
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "## Approved at ") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
