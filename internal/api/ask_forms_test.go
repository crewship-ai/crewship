package api

// The HTTP half of per-agent ask forms: the agent PATCH that writes the
// column and the agent GET that reads it back, plus the cross-workspace 404
// the neighbouring agent routes give.
//
// The rules themselves are tested in internal/askforms. What is tested here
// is that the endpoint actually runs them, actually stores the canonical
// form, and writes nothing at all when it refuses — the three things a
// validator in a package nobody calls would also pass.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const receiptForm = `[{"id":"receipt","label":"Add a receipt",` +
	`"template":"Please file this receipt.\n\nSupplier: {{supplier}}\nAmount: {{amount}} {{amount_currency}}",` +
	`"attachment":"required",` +
	`"fields":[` +
	`{"name":"supplier","label":"Supplier","type":"text","required":true,"placeholder":"Vodafone"},` +
	`{"name":"amount","label":"Amount","type":"money","required":true,"currency":["CZK","EUR","USD"]}` +
	`]}]`

func TestAgentUpdatePersistsAskForms(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-af", wsID, "", "Ann", "ann", "AGENT")

	body, err := json.Marshal(map[string]string{"ask_forms": receiptForm})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-af", string(body))
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH ask_forms = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	var got struct {
		AskForms *string `json:"ask_forms"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode PATCH response: %v", err)
	}
	if got.AskForms == nil {
		t.Fatal("PATCH response has no ask_forms — the UI reads the value back from here")
	}
	// Stored canonically: the default attachment policy of a form that did
	// not name one is spelled out, and the document is indented, so what the
	// author reads back on the next load is stable.
	if !strings.Contains(*got.AskForms, `"attachment": "required"`) {
		t.Errorf("stored ask_forms is not canonical JSON:\n%s", *got.AskForms)
	}

	var stored string
	if err := h.db.QueryRow(`SELECT ask_forms FROM agents WHERE id = 'ag-af'`).Scan(&stored); err != nil {
		t.Fatalf("read back ask_forms: %v", err)
	}
	if stored != *got.AskForms {
		t.Errorf("response and column disagree:\nresponse: %s\nstored:   %s", *got.AskForms, stored)
	}

	// And the detail endpoint returns it independently of the PATCH reply.
	detail := agentDetailJSON(t, h, userID, wsID, "ag-af")
	if detail["ask_forms"] != stored {
		t.Errorf("GET ask_forms = %v, want the stored document", detail["ask_forms"])
	}
}

func TestAgentUpdateClearsAskForms(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-af-clear", wsID, "", "Ann", "ann", "AGENT")

	seed, _ := json.Marshal(map[string]string{"ask_forms": receiptForm})
	if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-af-clear", string(seed)); rr.Code != http.StatusOK {
		t.Fatalf("seed PATCH = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	// Both ways of saying "none": an emptied editor and an explicit empty
	// array. Each must land on NULL, so "not configured" has one value.
	for _, clearing := range []string{`{"ask_forms":"   "}`, `{"ask_forms":"[]"}`, `{"ask_forms":null}`} {
		if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-af-clear", clearing); rr.Code != http.StatusOK {
			t.Fatalf("clearing PATCH %s = %d (%s), want 200", clearing, rr.Code, rr.Body.String())
		}
		var stored *string
		if err := h.db.QueryRow(`SELECT ask_forms FROM agents WHERE id = 'ag-af-clear'`).Scan(&stored); err != nil {
			t.Fatalf("read back: %v", err)
		}
		if stored != nil {
			t.Fatalf("after %s the column holds %q, want NULL", clearing, *stored)
		}
		// Put it back so the next clearing shape has something to clear.
		if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-af-clear", string(seed)); rr.Code != http.StatusOK {
			t.Fatalf("re-seed PATCH = %d, want 200", rr.Code)
		}
	}
}

func TestAgentUpdateAskFormsValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		forms   string
		wantMsg string
	}{
		{
			name:    "a placeholder naming no field is refused at save time",
			forms:   `[{"id":"receipt","label":"Add a receipt","template":"Supplier: {{suplier}}","fields":[{"name":"supplier","label":"Supplier","type":"text"}]}]`,
			wantMsg: `form "receipt": template names {{suplier}}`,
		},
		{
			name:    "a form with no fields",
			forms:   `[{"id":"receipt","label":"R","template":"hi","fields":[]}]`,
			wantMsg: "has no fields",
		},
		{
			name:    "a duplicate field name",
			forms:   `[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"},{"name":"a","label":"B","type":"text"}]}]`,
			wantMsg: `two fields are named "a"`,
		},
		{
			name:    "a select with no options",
			forms:   `[{"id":"r","label":"R","template":"{{a}}","fields":[{"name":"a","label":"A","type":"select"}]}]`,
			wantMsg: "with no options",
		},
		{
			name:    "a colliding form id",
			forms:   `[{"id":"r","label":"One","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]},{"id":"r","label":"Two","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]}]`,
			wantMsg: `two forms share the id "r"`,
		},
		{
			name:    "more than four forms",
			forms:   `[{"id":"a","label":"A","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]},{"id":"b","label":"B","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]},{"id":"c","label":"C","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]},{"id":"d","label":"D","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]},{"id":"e","label":"E","template":"{{a}}","fields":[{"name":"a","label":"A","type":"text"}]}]`,
			wantMsg: "at most 4 forms",
		},
		{
			name:    "malformed JSON",
			forms:   `[{"id":`,
			wantMsg: "not valid JSON",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, userID, wsID := covAUHandler(t)
			seedAgentRow(t, h.db, "ag-bad", wsID, "", "Ann", "ann", "AGENT")

			body, err := json.Marshal(map[string]string{"ask_forms": tt.forms})
			if err != nil {
				t.Fatalf("marshal body: %v", err)
			}
			rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-bad", string(body))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("PATCH = %d (%s), want 400", rr.Code, rr.Body.String())
			}
			if msg := apiErrorMessage(t, rr.Body.Bytes()); !strings.Contains(msg, tt.wantMsg) {
				t.Fatalf("error = %q, want it to name %q — the author is mid-edit and "+
					"needs to know which form and which placeholder", msg, tt.wantMsg)
			}
			var stored *string
			if err := h.db.QueryRow(`SELECT ask_forms FROM agents WHERE id = 'ag-bad'`).Scan(&stored); err != nil {
				t.Fatalf("read back: %v", err)
			}
			if stored != nil {
				t.Fatalf("ask_forms = %q after a rejected PATCH, want NULL", *stored)
			}
		})
	}
}

func TestAgentUpdateAskFormsWrongType(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-type", wsID, "", "Ann", "ann", "AGENT")

	// A caller sending the array itself rather than the document as a string
	// gets told which one this endpoint takes.
	rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-type", `{"ask_forms":[{"id":"r"}]}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH = %d (%s), want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "must be a string") {
		t.Errorf("body = %s, want it to say the field is a string", rr.Body.String())
	}
}

func TestAgentUpdateAskFormsCrossWorkspace404(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-af-mine", wsID, "", "Ann", "ann", "AGENT")

	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws-af-other', 'Other', 'other')`); err != nil {
		t.Fatalf("seed other workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-af-other', 'ws-af-other', ?, 'OWNER')`,
		userID); err != nil {
		t.Fatalf("seed other member: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"ask_forms": receiptForm})
	rr := covAUPatch(t, h, userID, "ws-af-other", "OWNER", "ag-af-mine", string(body))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace PATCH = %d (%s), want 404 — never a 403 that "+
			"confirms the id exists somewhere", rr.Code, rr.Body.String())
	}

	var stored *string
	if err := h.db.QueryRow(`SELECT ask_forms FROM agents WHERE id = 'ag-af-mine'`).Scan(&stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored != nil {
		t.Fatalf("ask_forms = %q after a cross-workspace PATCH, want NULL", *stored)
	}
}

// The chat page resolves its agent out of GET /agents by slug and never
// fetches the detail, so a column that only the detail response carries is a
// column the composer cannot see.
func TestAgentListCarriesAskForms(t *testing.T) {
	h, userID, wsID := covAUHandler(t)
	seedAgentRow(t, h.db, "ag-af-list", wsID, "", "Ann", "ann", "AGENT")

	body, _ := json.Marshal(map[string]string{"ask_forms": receiptForm})
	if rr := covAUPatch(t, h, userID, wsID, "OWNER", "ag-af-list", string(body)); rr.Code != http.StatusOK {
		t.Fatalf("seed PATCH = %d (%s), want 200", rr.Code, rr.Body.String())
	}

	r := httptest.NewRequest("GET", "/api/v1/agents?workspace_id="+wsID, nil)
	r = withWorkspaceUser(r, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET agents = %d (%s), want 200", rr.Code, rr.Body.String())
	}
	var list []map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		// The list handler may wrap its rows; unwrap before giving up.
		var wrapped struct {
			Agents []map[string]interface{} `json:"agents"`
			Data   []map[string]interface{} `json:"data"`
		}
		if err2 := json.Unmarshal(rr.Body.Bytes(), &wrapped); err2 != nil {
			t.Fatalf("decode list response: %v / %v (%s)", err, err2, rr.Body.String())
		}
		list = wrapped.Agents
		if list == nil {
			list = wrapped.Data
		}
	}

	for _, item := range list {
		if item["id"] != "ag-af-list" {
			continue
		}
		raw, ok := item["ask_forms"].(string)
		if !ok || !strings.Contains(raw, `"id": "receipt"`) {
			t.Fatalf("list ask_forms = %v, want the stored document", item["ask_forms"])
		}
		return
	}
	t.Fatal("seeded agent missing from the list response")
}

// apiErrorMessage pulls the message out of the {"error": "..."} envelope.
// Asserting on the raw body would compare against JSON-escaped quotes, and
// every message worth asserting on here names a form or a field in quotes.
func apiErrorMessage(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, body)
	}
	return out.Error
}
