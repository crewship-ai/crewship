package api

// escalation_journal_redaction_test.go — issue #2238: CreateEscalation
// dual-writes an escalation into inbox_items AND journal_entries
// (peer.escalation). The inbox copy is redacted (inbox.RedactSecrets) and
// its title is bounded (truncate(...,80)) — see escalation_handler.go
// ~line 227-250. The journal copy, ~40 lines later, was neither: its
// Payload carried body.Reason / body.Context / body.Metadata raw and
// unbounded, and its Summary was bounded but not redacted.
//
// That asymmetry is backwards. inbox_items is hard-deleted by the GDPR
// erasure cascade (admin_gdpr.go: `DELETE FROM inbox_items WHERE
// workspace_id = ? AND data_subject_id = ?`). journal_entries is
// explicitly EXCLUDED from erasure as accountability data, and is
// append-only / hash-chained, so nothing can clean it up after the fact.
// So the copy that gets deleted on request was the cleaned one, and the
// copy that survives every erasure forever held the raw text.
//
// It's also worse than a generic secret-logging bug: this route
// (POST /api/v1/internal/escalations) is internal sidecar IPC, which
// bypasses BodyCap (router.go ~813-818) entirely — body.Context in
// particular is an arbitrary-size, agent-supplied blob with no upstream
// size limit of any kind before it would otherwise land in a permanent
// hash-chained row.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// journalSecretCanary is shaped to match lookout's Anthropic-key rule
// (sk-ant-[A-Za-z0-9_-]{40,}) so RedactSecrets/lookout.Redact actually
// fires on it — a generic "secret" string wouldn't exercise the redactor.
const journalSecretCanary = "sk-ant-" + "a1B2c3D4e5F6g7H8i9J0k1L2m3N4o5P6q7R8s9T0" //gitleaks:allow — fake test fixture, asserts redaction

// journalOversizedBlob is comfortably larger than any reasonable per-field
// cap, standing in for the "agent stuffs an unbounded blob into context"
// scenario the issue calls out — this route has no BodyCap.
var journalOversizedBlob = strings.Repeat("x", 50_000)

// createEscForJournal posts CreateEscalation, flushes the journal writer,
// and returns the decoded payload + summary of the resulting
// peer.escalation journal_entries row.
func createEscForJournal(t *testing.T, h *QueryHandler, jw *journal.Writer, wsID string, body map[string]string) (payload map[string]any, summary string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/internal/escalations", jsonBody(body))
	req = req.WithContext(context.WithValue(req.Context(), ctxInternalTokenWS, wsID))
	rr := httptest.NewRecorder()
	h.CreateEscalation(rr, req)
	if rr.Code != 201 {
		t.Fatalf("CreateEscalation status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	if err := jw.Flush(context.Background()); err != nil {
		t.Fatalf("flush journal: %v", err)
	}
	var payloadStr string
	if err := h.db.QueryRow(
		`SELECT summary, payload FROM journal_entries
		 WHERE workspace_id = ? AND entry_type = ?
		 ORDER BY rowid DESC LIMIT 1`,
		wsID, string(journal.EntryPeerEscalation),
	).Scan(&summary, &payloadStr); err != nil {
		t.Fatalf("load peer.escalation journal entry: %v", err)
	}
	if err := json.Unmarshal([]byte(payloadStr), &payload); err != nil {
		t.Fatalf("unmarshal journal payload %q: %v", payloadStr, err)
	}
	return payload, summary
}

// journalFieldBoundLen is the ceiling the fix must enforce on each
// agent-supplied journal payload field (reason/context/metadata). Kept in
// the test as a literal, not imported from the fix, so this test pins the
// externally-observable contract rather than whatever constant name the
// implementation happens to pick.
const journalFieldBoundLen = 4096

func TestCreateEscalation_JournalPayload_RedactsAndBoundsAgentSuppliedFields(t *testing.T) {
	cases := []struct {
		name  string
		field string
		body  map[string]string
	}{
		{
			name:  "reason: secret-shaped value must not reach the journal in the clear",
			field: "reason",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": "leaking a key: " + journalSecretCanary,
				"chat_id": "covesc-chat", "context": "ctx", "metadata": "",
			},
		},
		{
			name:  "context: secret-shaped value must not reach the journal in the clear",
			field: "context",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": "need approval",
				"chat_id": "covesc-chat", "context": "background: " + journalSecretCanary, "metadata": "",
			},
		},
		{
			name:  "metadata: secret-shaped value must not reach the journal in the clear",
			field: "metadata",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": "need approval",
				"chat_id": "covesc-chat", "context": "ctx", "metadata": "note: " + journalSecretCanary,
			},
		},
		{
			name:  "reason: oversized agent-supplied blob must not reach the journal unbounded",
			field: "reason",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": journalOversizedBlob,
				"chat_id": "covesc-chat", "context": "ctx", "metadata": "",
			},
		},
		{
			name:  "context: oversized agent-supplied blob must not reach the journal unbounded",
			field: "context",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": "need approval",
				"chat_id": "covesc-chat", "context": journalOversizedBlob, "metadata": "",
			},
		},
		{
			name:  "metadata: oversized agent-supplied blob must not reach the journal unbounded",
			field: "metadata",
			body: map[string]string{
				"from_slug": "covesc-ag", "reason": "need approval",
				"chat_id": "covesc-chat", "context": "ctx", "metadata": journalOversizedBlob,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, crewID, _ := covEscFixture(t)
			seedChat(t, h, "covesc-chat", "covesc-ag", wsID)
			jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
			t.Cleanup(func() { _ = jw.Close() })
			h.SetJournal(jw)

			body := tc.body
			body["crew_id"] = crewID
			body["workspace_id"] = wsID

			payload, summary := createEscForJournal(t, h, jw, wsID, body)

			raw, ok := payload[tc.field]
			if !ok {
				t.Fatalf("journal payload missing field %q: %v", tc.field, payload)
			}
			str, ok := raw.(string)
			if !ok {
				t.Fatalf("journal payload field %q is not a string: %T %v", tc.field, raw, raw)
			}

			if strings.Contains(str, journalSecretCanary) {
				t.Errorf("journal payload[%q] leaked the raw secret in the clear: %q", tc.field, str)
			}
			if strings.Contains(summary, journalSecretCanary) {
				t.Errorf("journal entry Summary leaked the raw secret in the clear: %q", summary)
			}
			if n := len([]rune(str)); n > journalFieldBoundLen+1 {
				t.Errorf("journal payload[%q] is unbounded: %d runes (want <= %d)", tc.field, n, journalFieldBoundLen+1)
			}
		})
	}
}

// TestCreateEscalation_JournalSummary_Redacted pins the Summary line
// specifically: it was already bounded (truncate(...,140)) before this
// fix, but not redacted, so a secret near the front of Reason rode along
// into the permanent, human-visible summary untouched.
func TestCreateEscalation_JournalSummary_Redacted(t *testing.T) {
	h, _, wsID, crewID, _ := covEscFixture(t)
	seedChat(t, h, "covesc-chat", "covesc-ag", wsID)
	jw := journal.NewWriter(h.db, newTestLogger(), journal.WriterOptions{FlushSize: 1})
	t.Cleanup(func() { _ = jw.Close() })
	h.SetJournal(jw)

	body := map[string]string{
		"from_slug": "covesc-ag", "reason": journalSecretCanary + " needs approval",
		"crew_id": crewID, "workspace_id": wsID, "chat_id": "covesc-chat",
	}
	_, summary := createEscForJournal(t, h, jw, wsID, body)
	if strings.Contains(summary, journalSecretCanary) {
		t.Errorf("journal entry Summary leaked the raw secret in the clear: %q", summary)
	}
}
