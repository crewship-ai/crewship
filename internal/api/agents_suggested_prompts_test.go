package api

// Tests for the per-agent suggested-prompts column (PRD
// chat-as-a-primary-surface, Step 7).
//
// Two halves: the pure normaliser (table-driven, every shape of input a
// textarea can produce) and the two HTTP paths that carry the value — the
// agent PATCH that writes it and the agent GET that reads it back, plus the
// cross-workspace 404 the rest of the agent routes give.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeSuggestedPrompts(t *testing.T) {
	long120 := strings.Repeat("a", 120)
	long121 := strings.Repeat("a", 121)
	// 120 runes of a 2-byte character: the cap is characters, not bytes,
	// so this must pass where a byte-length check would reject it.
	wide120 := strings.Repeat("é", 120)

	eight := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{
			name: "empty is empty, not an error",
			in:   "",
			want: "",
		},
		{
			name: "whitespace only collapses to empty",
			in:   "   \n\t\n  \n",
			want: "",
		},
		{
			name: "single prompt",
			in:   "What did you do yesterday?",
			want: "What did you do yesterday?",
		},
		{
			name: "eight prompts is the cap and passes",
			in:   strings.Join(eight, "\n"),
			want: strings.Join(eight, "\n"),
		},
		{
			name:    "nine prompts is rejected by count",
			in:      strings.Join(append(append([]string{}, eight...), "nine"), "\n"),
			wantErr: "at most 8 prompts",
		},
		{
			name: "a prompt of exactly 120 characters passes",
			in:   long120,
			want: long120,
		},
		{
			name:    "a prompt of 121 characters is rejected, by position",
			in:      "fine\nalso fine\n" + long121,
			wantErr: "prompt 3 exceeds 120 characters",
		},
		{
			name: "120 multi-byte characters pass — the cap counts characters",
			in:   wide120,
			want: wide120,
		},
		{
			name: "blank lines between entries are ignored",
			in:   "first\n\n\nsecond\n\nthird\n",
			want: "first\nsecond\nthird",
		},
		{
			name: "CRLF line endings are normalised",
			in:   "first\r\nsecond\r\n\r\nthird\r\n",
			want: "first\nsecond\nthird",
		},
		{
			name: "leading and trailing whitespace is trimmed per line",
			in:   "  first  \n\t second\t\n   third   ",
			want: "first\nsecond\nthird",
		},
		{
			name: "a line that is only whitespace is dropped, not counted",
			in:   "first\n     \nsecond",
			want: "first\nsecond",
		},
		{
			name: "whitespace-only lines do not count towards the cap",
			in:   strings.Join(eight, "\n   \n"),
			want: strings.Join(eight, "\n"),
		},
		{
			name: "bare CR is treated as a line break too",
			in:   "first\rsecond",
			want: "first\nsecond",
		},
		{
			name: "trailing whitespace is trimmed before the length check",
			in:   long120 + "                    ",
			want: long120,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeSuggestedPrompts(tt.in)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("normalizeSuggestedPrompts(%q) = %q, want error %q", tt.in, got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSuggestedPrompts(%q) errored: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeSuggestedPrompts(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// ---- HTTP: the PATCH writes it and the GET reads it back ----

func TestAgentUpdatePersistsSuggestedPrompts(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-sp", wsID, "", "Ann", "ann", "AGENT")

	body := `{"suggested_prompts":"  What shipped this week?\r\n\r\nWho is blocked?  \n"}`
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-sp", body)
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH suggested_prompts = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	// Update replies with the agent detail, so the response is also the
	// read path's assertion.
	var got struct {
		SuggestedPrompts *string `json:"suggested_prompts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode PATCH response: %v", err)
	}
	const want = "What shipped this week?\nWho is blocked?"
	if got.SuggestedPrompts == nil || *got.SuggestedPrompts != want {
		t.Fatalf("PATCH response suggested_prompts = %v, want %q", got.SuggestedPrompts, want)
	}

	var stored string
	if err := h.db.QueryRow(`SELECT suggested_prompts FROM agents WHERE id = 'ag-sp'`).Scan(&stored); err != nil {
		t.Fatalf("read back suggested_prompts: %v", err)
	}
	if stored != want {
		t.Fatalf("stored suggested_prompts = %q, want %q (normalised on write)", stored, want)
	}

	// And the detail endpoint returns it independently of the PATCH reply.
	detail := agentDetailJSON(t, h, userID, wsID, "ag-sp")
	if detail["suggested_prompts"] != want {
		t.Fatalf("GET suggested_prompts = %v, want %q", detail["suggested_prompts"], want)
	}
}

func TestAgentUpdateClearsSuggestedPrompts(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-clear", wsID, "", "Ann", "ann", "AGENT")

	if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-clear",
		`{"suggested_prompts":"one\ntwo"}`); rr.Code != http.StatusOK {
		t.Fatalf("seed PATCH = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-clear",
		`{"suggested_prompts":"   "}`); rr.Code != http.StatusOK {
		t.Fatalf("clearing PATCH = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	// An emptied textarea stores NULL, not "" — one representation of
	// "unset", so the fallback to the role packs has one condition to test.
	var stored *string
	if err := h.db.QueryRow(`SELECT suggested_prompts FROM agents WHERE id = 'ag-clear'`).Scan(&stored); err != nil {
		t.Fatalf("read back suggested_prompts: %v", err)
	}
	if stored != nil {
		t.Fatalf("stored suggested_prompts = %q, want NULL after clearing", *stored)
	}
}

func TestAgentUpdateSuggestedPromptsValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{
			name:    "too many",
			body:    `{"suggested_prompts":"a\nb\nc\nd\ne\nf\ng\nh\ni"}`,
			wantMsg: "at most 8 prompts",
		},
		{
			name:    "too long, named by position",
			body:    `{"suggested_prompts":"short\n` + strings.Repeat("x", 121) + `"}`,
			wantMsg: "prompt 2 exceeds 120 characters",
		},
		{
			name:    "wrong type",
			body:    `{"suggested_prompts":42}`,
			wantMsg: "suggested_prompts must be a string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedAgentRow(t, h.db, "ag-bad", wsID, "", "Ann", "ann", "AGENT")

			rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-bad", tt.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("PATCH = %d (%s), want 400", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tt.wantMsg) {
				t.Fatalf("body = %s, want it to name %q — a generic \"invalid input\" is not actionable",
					rr.Body.String(), tt.wantMsg)
			}
			// Nothing may have been written on a rejected request.
			var stored *string
			if err := h.db.QueryRow(`SELECT suggested_prompts FROM agents WHERE id = 'ag-bad'`).Scan(&stored); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if stored != nil {
				t.Fatalf("suggested_prompts = %q after a rejected PATCH, want NULL", *stored)
			}
		})
	}
}

func TestAgentUpdateSuggestedPromptsCrossWorkspace404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-mine", wsID, "", "Ann", "ann", "AGENT")

	// A second workspace the caller also owns. The agent is not in it, so
	// the PATCH must 404 exactly like every other agent route — never 200,
	// and never a 403 that confirms the id exists somewhere.
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', 'ws-other', ?, 'OWNER')`,
		userID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	rr := covAUPatch(t, h, userID, "ws-other", "OWNER", "ag-mine", `{"suggested_prompts":"leaked"}`)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace PATCH = %d (%s), want 404", rr.Code, rr.Body.String())
	}

	var stored *string
	if err := h.db.QueryRow(`SELECT suggested_prompts FROM agents WHERE id = 'ag-mine'`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != nil {
		t.Fatalf("suggested_prompts = %q after a cross-workspace PATCH, want NULL", *stored)
	}
}

// agentDetailJSON drives AgentHandler.Get and returns the decoded body.
func agentDetailJSON(t *testing.T, h *AgentHandler, userID, wsID, agentID string) map[string]interface{} {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/v1/agents/"+agentID, nil)
	r.SetPathValue("agentId", agentID)
	r = withWorkspaceUser(r, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Get(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET agent = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var out map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	return out
}
