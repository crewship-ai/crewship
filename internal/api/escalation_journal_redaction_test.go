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
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// TestExpireEscalationRow_JournalSummary_Redacted — the sweeper
// (escalation_lifecycle.go's expireEscalationRow) writes a SECOND
// peer.escalation journal entry when a PENDING escalation's answer
// deadline passes unanswered, built from `row.reason` read straight back
// out of the escalations table. That is the exact same agent-supplied
// field CreateEscalation redacts before journaling — but the sweep path
// wrote `truncate(row.reason, 140)` with no redaction, so a credential
// that arrived as the FIRST few words of `reason` and was never answered
// in time rode into a second permanent entry unredacted. Same leak
// (#2238), different route into the same journal.
func TestExpireEscalationRow_JournalSummary_Redacted(t *testing.T) {
	h, rec, _, wsID, crewID, agentID := escLifecycleRig(t)
	past := time.Now().UTC().Add(-time.Hour)
	execOrFatal(t, h.db, `INSERT INTO escalations
		(id, workspace_id, crew_id, chat_id, from_agent_id, reason, type, status,
		 deadline_at, answer_deadline_at, created_at)
		VALUES (?, ?, ?, 'lc-chat', ?, ?, 'TEXT', 'PENDING', ?, ?, ?)`,
		"lc-expire-secret", wsID, crewID, agentID,
		journalSecretCanary+" needs the API key",
		past.Format(time.RFC3339), past.Format(time.RFC3339),
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339))

	if _, err := h.sweepExpiredEscalations(context.Background(), wsID); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	var summary string
	found := false
	for _, e := range rec.entries {
		if e.ActorID == "escalation_deadline" {
			summary = e.Summary
			found = true
		}
	}
	if !found {
		t.Fatalf("no expiry peer.escalation journal entry recorded: %+v", rec.entries)
	}
	if strings.Contains(summary, journalSecretCanary) {
		t.Errorf("expiry journal entry Summary leaked the raw secret in the clear: %q", summary)
	}
}

// TestResolveEscalation_JournalResolution_RedactsAndBounds — the resolve
// path's `resolutionForJournal` was only ever redacted for
// escalationType == "CREDENTIAL" (a flat marker, since the actual secret is
// encrypted separately), and was never length-bounded for any type. An
// admin resolving a non-CREDENTIAL escalation with free text that happens
// to contain a secret-shaped value ("used token sk-ant-... to unblock it")
// put it, verbatim and unbounded, into a permanent journal entry visible to
// every workspace reader. It needs the same RedactSecrets-then-truncate
// treatment CreateEscalation already gives reason/context/metadata (#2238).
func TestResolveEscalation_JournalResolution_RedactsAndBounds(t *testing.T) {
	h, userID, wsID, crewID, _ := covEscFixture(t)
	seedChat(t, h, "covesc-chat", "covesc-ag", wsID)
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need approval", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "TEXT",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create escalation: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		EscalationID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	cases := []struct {
		name       string
		resolution string
	}{
		{
			name:       "secret-shaped resolution text must not reach the journal in the clear",
			resolution: "used token " + journalSecretCanary + " to unblock it",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr2 := covEscResolve(h, userID, wsID, created.EscalationID, map[string]string{
				"resolution": tc.resolution,
				"action":     "approve",
			})
			if rr2.Code != http.StatusOK {
				t.Fatalf("resolve status = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
			}

			var payload map[string]any
			found := false
			for _, e := range rec.entries {
				if e.Type == journal.EntryPeerEscalation && e.Payload != nil {
					if state, _ := e.Payload["state"].(string); state == "resolved" {
						payload = e.Payload
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("no resolution peer.escalation journal entry recorded: %+v", rec.entries)
			}
			res, ok := payload["resolution"].(string)
			if !ok {
				t.Fatalf("resolution journal payload missing/not a string: %v", payload)
			}
			if strings.Contains(res, journalSecretCanary) {
				t.Errorf("resolution journal payload leaked the raw secret in the clear: %q", res)
			}
		})
	}
}

// TestResolveEscalation_JournalResolution_Bounded pins the length ceiling
// directly: an unbounded `resolution` field bypasses no BodyCap (this route
// is a normal authenticated JSON endpoint, but nothing capped this specific
// field before the fix) and lands in the same permanent, hash-chained entry
// the sweeper and CreateEscalation now both bound.
func TestResolveEscalation_JournalResolution_Bounded(t *testing.T) {
	h, userID, wsID, crewID, _ := covEscFixture(t)
	seedChat(t, h, "covesc-chat", "covesc-ag", wsID)
	rec := &recordingEmitter{}
	h.SetJournal(rec)

	rr := createEsc(h, wsID, map[string]string{
		"from_slug": "covesc-ag", "reason": "need approval", "crew_id": crewID,
		"workspace_id": wsID, "chat_id": "covesc-chat", "type": "TEXT",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create escalation: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		EscalationID string `json:"escalation_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	rr2 := covEscResolve(h, userID, wsID, created.EscalationID, map[string]string{
		"resolution": journalOversizedBlob,
		"action":     "approve",
	})
	if rr2.Code != http.StatusOK {
		t.Fatalf("resolve status = %d, want 200; body=%s", rr2.Code, rr2.Body.String())
	}

	var payload map[string]any
	found := false
	for _, e := range rec.entries {
		if e.Type == journal.EntryPeerEscalation && e.Payload != nil {
			if state, _ := e.Payload["state"].(string); state == "resolved" {
				payload = e.Payload
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no resolution peer.escalation journal entry recorded: %+v", rec.entries)
	}
	res, ok := payload["resolution"].(string)
	if !ok {
		t.Fatalf("resolution journal payload missing/not a string: %v", payload)
	}
	if n := len([]rune(res)); n > journalFieldBoundLen+1 {
		t.Errorf("resolution journal payload is unbounded: %d runes (want <= %d)", n, journalFieldBoundLen+1)
	}
}
