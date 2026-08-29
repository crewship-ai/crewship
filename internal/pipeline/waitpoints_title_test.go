package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

// The inbox row's title, and the risk level beside it — #2160.
//
// The bug this pins: the title was CleanTitle(prompt), and an approval
// prompt is authored boilerplate ("Approve this production action?"), so
// every invocation of a routine produced a byte-identical row. Three
// pending approvals for a credential rotation, a bucket deletion and a
// scale-out were indistinguishable in the list, and nothing on the row
// said what any of them would do.
//
// Driven through CreateApproval rather than the executor because that is
// where the projection is written; runner_wait_title_test.go covers the
// other half — that a templated approval_title is rendered against the
// run's context before it gets here.

func TestCreateApproval_InboxTitle(t *testing.T) {
	t.Parallel()

	const prompt = "Approve this production action?\n\n## Change Plan\n\nRestart the pods."

	cases := []struct {
		name      string
		title     string
		risk      string
		wantTitle string
		wantRisk  string
	}{
		{
			// The fallback every routine written before approval_title
			// existed still gets: the prompt's first line, markdown
			// stripped. Identical across runs, which is the defect —
			// but it must keep working, not start erroring.
			name:      "no title falls back to the prompt's first line",
			wantTitle: "Approve this production action?",
			wantRisk:  "normal",
		},
		{
			name:      "authored title wins over the prompt",
			title:     "Scale payments-api to 12 replicas",
			wantTitle: "Scale payments-api to 12 replicas",
			wantRisk:  "normal",
		},
		{
			// Whitespace-only is not a title. Falling through to the
			// prompt beats rendering a blank row.
			name:      "blank title falls back",
			title:     "   \n\t ",
			wantTitle: "Approve this production action?",
			wantRisk:  "normal",
		},
		{
			// The title is templated, so it can carry whatever the run's
			// inputs held — including a secret. It goes through the same
			// redaction the body does. The fixture is full length on
			// purpose: lookout's Anthropic pattern wants 40+ chars after
			// the prefix, and a short stand-in would pass this test while
			// proving nothing about a real key.
			name:      "secrets in the title are redacted",
			title:     "Rotate sk-ant-api03-" + strings.Repeat("A", 44) + " now",
			wantTitle: "REDACTED",
			wantRisk:  "normal",
		},
		{
			name:      "risk level is carried through",
			title:     "Delete staging bucket",
			risk:      "destructive",
			wantTitle: "Delete staging bucket",
			wantRisk:  "destructive",
		},
		{
			// Unset means normal, defaulted at the write so every reader
			// sees the same value rather than inventing its own.
			name:      "unset risk defaults to normal",
			title:     "Scale out",
			risk:      "",
			wantTitle: "Scale out",
			wantRisk:  "normal",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTrustGateTestDB(t)
			store := NewSQLWaitpointStore(db)
			seedTrustRun(t, db, "run_"+strings.ReplaceAll(tc.name, " ", "_"), "hash1", "")

			token, err := store.CreateApproval(t.Context(), WaitpointApprovalRequest{
				WorkspaceID:   "ws_test",
				PipelineRunID: "run_" + strings.ReplaceAll(tc.name, " ", "_"),
				StepID:        "approve",
				Prompt:        prompt,
				Title:         tc.title,
				RiskLevel:     tc.risk,
			})
			if err != nil {
				t.Fatalf("CreateApproval: %v", err)
			}

			var gotTitle, gotPayload string
			if err := db.QueryRowContext(t.Context(),
				`SELECT title, payload_json FROM inbox_items WHERE source_id = ?`, token,
			).Scan(&gotTitle, &gotPayload); err != nil {
				t.Fatalf("read inbox row: %v", err)
			}

			if tc.wantTitle == "REDACTED" {
				if strings.Contains(gotTitle, strings.Repeat("A", 44)) {
					t.Errorf("title leaked a secret: %q", gotTitle)
				}
			} else if gotTitle != tc.wantTitle {
				t.Errorf("title = %q, want %q", gotTitle, tc.wantTitle)
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(gotPayload), &payload); err != nil {
				t.Fatalf("payload not JSON: %v (%s)", err, gotPayload)
			}
			if got, _ := payload["risk_level"].(string); got != tc.wantRisk {
				t.Errorf("payload.risk_level = %q, want %q", got, tc.wantRisk)
			}
		})
	}
}

// A title longer than the row can show is truncated on the same path the
// fallback is, so one cannot overflow where the other does not.
func TestCreateApproval_InboxTitle_Truncated(t *testing.T) {
	t.Parallel()

	db := openTrustGateTestDB(t)
	store := NewSQLWaitpointStore(db)
	seedTrustRun(t, db, "run_long", "hash1", "")

	long := strings.Repeat("scale the payments api ", 20)
	token, err := store.CreateApproval(t.Context(), WaitpointApprovalRequest{
		WorkspaceID:   "ws_test",
		PipelineRunID: "run_long",
		StepID:        "approve",
		Prompt:        "Approve?",
		Title:         long,
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	var got string
	if err := db.QueryRowContext(t.Context(),
		`SELECT title FROM inbox_items WHERE source_id = ?`, token).Scan(&got); err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	if n := len([]rune(got)); n > 80 {
		t.Errorf("title is %d runes, want <= 80 (same cap as the prompt fallback)", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("truncated title %q should end in an ellipsis", got)
	}
}

// A multi-line title collapses to one line. Nothing stops an author
// writing one, and the row is a single line of text.
func TestCreateApproval_InboxTitle_SingleLine(t *testing.T) {
	t.Parallel()

	db := openTrustGateTestDB(t)
	store := NewSQLWaitpointStore(db)
	seedTrustRun(t, db, "run_multiline", "hash1", "")

	token, err := store.CreateApproval(t.Context(), WaitpointApprovalRequest{
		WorkspaceID:   "ws_test",
		PipelineRunID: "run_multiline",
		StepID:        "approve",
		Prompt:        "Approve?",
		Title:         "Scale payments-api\nto 12 replicas",
	})
	if err != nil {
		t.Fatalf("CreateApproval: %v", err)
	}

	var got string
	if err := db.QueryRowContext(t.Context(),
		`SELECT title FROM inbox_items WHERE source_id = ?`, token).Scan(&got); err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("title %q contains a newline", got)
	}
	if got != "Scale payments-api" {
		t.Errorf("title = %q, want the first line", got)
	}
}
