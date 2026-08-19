package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// "Who did this?" is the first question anyone asks of an audit log, and the
// timeline could not answer it: the actor was recorded as an `agent_id` column
// on one path and under five different metadata keys on the others. These pin
// the resolution, because getting it subtly wrong — attributing a reveal to the
// operator who created the credential, say — is worse than leaving it blank.

func TestResolveAuditActor_AgentColumnWinsOverMetadata(t *testing.T) {
	agent := "ag_1"
	// An agent-driven read can carry the operator who set the automation up.
	// The column is the foreign key the database enforces; the metadata is
	// free-form, so the column decides.
	kind, id := resolveAuditActor(&agent, map[string]any{"created_by": "user_9"})
	if kind != "agent" || id != "ag_1" {
		t.Errorf("got (%s, %s), want (agent, ag_1)", kind, id)
	}
}

func TestResolveAuditActor_ReadsEveryHumanKeyWeHaveEverWritten(t *testing.T) {
	for _, key := range auditActorMetadataKeys {
		kind, id := resolveAuditActor(nil, map[string]any{key: "user_7"})
		if kind != "user" || id != "user_7" {
			t.Errorf("%s: got (%s, %s), want (user, user_7)", key, kind, id)
		}
	}
}

// A sidecar serves a whole container, so there is no agent to name — but the
// crew that owns it is a real answer where "system" is only an admission.
func TestResolveAuditActor_SidecarFetchIsAttributedToItsCrew(t *testing.T) {
	kind, id := resolveAuditActor(nil, map[string]any{"source": "sidecar_fetch", "crew_id": "crew_1"})
	if kind != "crew" || id != "crew_1" {
		t.Errorf("got (%s, %s), want (crew, crew_1)", kind, id)
	}
}

// A human key still wins: a crew_id in the metadata of an operator action
// describes the scope, not the actor.
func TestResolveAuditActor_HumanKeyBeatsCrewScope(t *testing.T) {
	kind, id := resolveAuditActor(nil, map[string]any{"crew_id": "crew_1", "rotated_by": "u1"})
	if kind != "user" || id != "u1" {
		t.Errorf("got (%s, %s), want (user, u1)", kind, id)
	}
}

// "system" is the honest answer for a row nobody signed — not a placeholder
// standing in for a lookup that was skipped.
func TestResolveAuditActor_UnsignedRowIsSystem(t *testing.T) {
	for _, meta := range []map[string]any{nil, {}, {"grace_seconds": 3600}} {
		if kind, id := resolveAuditActor(nil, meta); kind != "system" || id != "" {
			t.Errorf("meta %v: got (%s, %s), want (system, )", meta, kind, id)
		}
	}
}

func TestResolveAuditActor_EmptyAgentIDIsNotAnAgent(t *testing.T) {
	empty := ""
	if kind, _ := resolveAuditActor(&empty, map[string]any{"rotated_by": "u1"}); kind != "user" {
		t.Errorf("kind = %s, want user — an empty agent_id names nobody", kind)
	}
}

// The endpoint has to carry the resolution through, with names attached, or the
// console is back to rendering ids at the reader.
func TestAuditTimeline_NamesTheActor(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	execOrFatal(t, db, `UPDATE users SET full_name = 'Riley Quinn' WHERE id = ?`, userID)
	execOrFatal(t, db, `INSERT INTO crews (id, workspace_id, name, slug) VALUES ('act-crew', ?, 'C', 'act-c')`, wsID)
	seedAgentRow(t, db, "act-ag", wsID, "act-crew", "Deploy bot", "act-a", "AGENT")
	seedCredentialEnc(t, db, wsID, userID, "act-cred", "ACT_KEY", "secret")

	execOrFatal(t, db, `INSERT INTO credential_audit (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES ('ev-use', 'act-cred', 'USE', 'act-ag', '10.0.0.1', NULL, '2026-08-10T10:00:00Z')`)
	execOrFatal(t, db, `INSERT INTO credential_audit (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES ('ev-reveal', 'act-cred', 'REVEAL', NULL, '10.0.0.2', ?, '2026-08-10T11:00:00Z')`,
		`{"revealed_by":"`+userID+`","reason":"incident"}`)
	execOrFatal(t, db, `INSERT INTO credential_audit (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES ('ev-sys', 'act-cred', 'DETECTED', NULL, NULL, '{}', '2026-08-10T09:00:00Z')`)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/credentials/act-cred/audit", nil)
	req.SetPathValue("credentialId", "act-cred")
	req = req.WithContext(auditTestCtx(req.Context(), wsID, userID))
	rec := httptest.NewRecorder()
	h.AuditTimeline(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out []auditEventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]auditEventResponse{}
	for _, e := range out {
		byID[e.ID] = e
	}

	if got := byID["ev-use"]; got.ActorKind != "agent" || got.ActorName != "Deploy bot" || got.ActorID != "act-ag" {
		t.Errorf("USE actor = %+v, want the agent by id and name", got)
	}
	// The reveal is the row that most needs a name on it.
	if got := byID["ev-reveal"]; got.ActorKind != "user" || got.ActorName != "Riley Quinn" {
		t.Errorf("REVEAL actor = %+v, want the human who asked for it", got)
	}
	if got := byID["ev-sys"]; got.ActorKind != "system" || got.ActorID != "" {
		t.Errorf("DETECTED actor = %+v, want system", got)
	}
}

// A deleted agent still did the thing. Dropping the row, or relabelling it
// "system", would rewrite history to hide the actor we no longer have a name
// for — so the id survives and only the name is blank.
func TestAuditTimeline_KeepsAnActorItCannotName(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "gone-cred", "GONE_KEY", "secret")
	execOrFatal(t, db, `INSERT INTO credential_audit (id, credential_id, event_type, agent_id, ip_address, metadata_json, occurred_at)
		VALUES ('ev-gone', 'gone-cred', 'ROTATE', NULL, NULL, '{"rotated_by":"user_vanished"}', '2026-08-10T10:00:00Z')`)

	h := NewCredentialHandler(db, newTestLogger())
	req := httptest.NewRequest(http.MethodGet, "/api/v1/credentials/gone-cred/audit", nil)
	req.SetPathValue("credentialId", "gone-cred")
	req = req.WithContext(auditTestCtx(req.Context(), wsID, userID))
	rec := httptest.NewRecorder()
	h.AuditTimeline(rec, req)

	var out []auditEventResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d events, want 1", len(out))
	}
	if out[0].ActorKind != "user" || out[0].ActorID != "user_vanished" || out[0].ActorName != "" {
		t.Errorf("actor = %+v, want the id kept and the name blank", out[0])
	}
}

/** The context the audit handler reads: workspace, caller and role. */
func auditTestCtx(ctx context.Context, wsID, userID string) context.Context {
	ctx = context.WithValue(ctx, ctxWorkspaceID, wsID)
	ctx = context.WithValue(ctx, ctxUser, &AuthUser{ID: userID})
	return context.WithValue(ctx, ctxRole, "OWNER")
}

// crew_id in metadata is not automatically an actor. Plenty of events could
// carry one as SCOPE — the crew a rotation applied to, say — and attributing
// those to a crew would be a confident wrong answer where "system" was merely a
// dull right one. Only the sidecar marker means "this crew's container read it".
func TestResolveAuditActor_CrewIDAloneIsNotAnActor(t *testing.T) {
	kind, id := resolveAuditActor(nil, map[string]any{"crew_id": "crew_1"})
	if kind != "system" || id != "" {
		t.Errorf("got (%s, %s), want (system, ) — a crew id without the sidecar marker names a scope, not an actor", kind, id)
	}
}
