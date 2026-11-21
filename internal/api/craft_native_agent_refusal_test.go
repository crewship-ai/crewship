package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// #5596: a task start refused after Page authority passed names its reason,
// so the author can fix it. Every refusal stays errCraftNativeAgentDenied.
func TestCraftNativeAgentTaskRefusalNamesReason(t *testing.T) {
	type fixture = *artifactPrincipalFixture
	for _, tc := range []struct {
		reason  string
		arrange func(t *testing.T, f fixture, m *craftNativeAgentManager, input *craftNativeAgentTaskRequest, ctx context.Context)
	}{
		{craftNativeAgentReasonPolicyDisabled, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
		}},
		{craftNativeAgentReasonPolicyDisabled, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `DELETE FROM workspace_artifact_creation_crews; DELETE FROM workspace_artifact_creation_policy`)
		}},
		{craftNativeAgentReasonCrewNotAllowed, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `DELETE FROM workspace_artifact_creation_crews`)
		}},
		{craftNativeAgentReasonChatUnavailable, func(_ *testing.T, _ fixture, _ *craftNativeAgentManager, input *craftNativeAgentTaskRequest, _ context.Context) {
			input.ChatID = "no-such-chat"
		}},
		{craftNativeAgentReasonChatNotPrivate, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, f.a.ChatID)
		}},
		{craftNativeAgentReasonInstructionNotRecorded, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET native_craft_instruction=0 WHERE id=?`, f.a.MessageID)
		}},
		{craftNativeAgentReasonInstructionNotRecorded, func(_ *testing.T, _ fixture, _ *craftNativeAgentManager, input *craftNativeAgentTaskRequest, _ context.Context) {
			input.MessageID = "no-such-message"
		}},
		{craftNativeAgentReasonInstructionUnusable, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET content='   ' WHERE id=?`, f.a.MessageID)
		}},
		{craftNativeAgentReasonAgentUnavailable, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE agents SET expired_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339), f.a.AgentID)
		}},
		{craftNativeAgentReasonNotOwnerOrDelegated, func(t *testing.T, f fixture, _ *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			mustExec(t, f.rig.db, `UPDATE workspace_members SET role='ADMIN' WHERE workspace_id=? AND user_id=?`, f.a.WorkspaceID, f.a.HumanUserID)
			mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
		}},
		{craftNativeAgentReasonDuplicateTask, func(t *testing.T, _ fixture, m *craftNativeAgentManager, input *craftNativeAgentTaskRequest, ctx context.Context) {
			if _, err := m.produceTask(ctx, "@native-agent-page", *input); err != nil {
				t.Fatal("first task", err)
			}
		}},
		{craftNativeAgentReasonCapacity, func(_ *testing.T, _ fixture, m *craftNativeAgentManager, _ *craftNativeAgentTaskRequest, _ context.Context) {
			m.mu.Lock()
			for i := range 128 {
				m.entries[fmt.Sprintf("filler-%d", i)] = &craftNativeAgentEntry{}
			}
			m.mu.Unlock()
		}},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			input := craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true, Mode: craftNativeAgentModeApply}
			tc.arrange(t, f, m, &input, ctx)
			accepted, err := m.produceTask(ctx, "@native-agent-page", input)
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("refusal lost the mandate sentinel: %+v %v", accepted, err)
			}
			if got := craftNativeAgentRefusalReason(err); got != tc.reason {
				t.Fatalf("reason = %q, want %q (%v)", got, tc.reason, err)
			}
			w := httptest.NewRecorder()
			craftNativeAgentTaskError(w, err)
			var body map[string]string
			if w.Code != 403 || json.Unmarshal(w.Body.Bytes(), &body) != nil || body["reason"] != tc.reason || body["error"] == "" || len(body) != 2 {
				t.Fatalf("refusal body: %d %s", w.Code, w.Body)
			}
		})
	}
}

// #5596: a caller without Page access learns nothing from the refusal: no
// reason, and the same answer whatever the policy, chat or registry state.
func TestCraftNativeAgentTaskRefusalWithoutPageAccessIsGeneric(t *testing.T) {
	answer := func(t *testing.T, slug string, arrange func(*artifactPrincipalFixture, *craftNativeAgentManager)) (int, string) {
		t.Helper()
		f, m, ctx := nativeAgentAdmissionFixture(t)
		// Another member's private document the caller cannot see.
		mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=?, private_audience=1 WHERE id='native-agent-page'`, f.rig.adminID)
		if arrange != nil {
			arrange(f, m)
		}
		_, err := m.produceTask(ctx, slug, craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true, Mode: craftNativeAgentModeApply})
		if err == nil {
			t.Fatal("task admitted without Page access")
		}
		if reason := craftNativeAgentRefusalReason(err); reason != "" {
			t.Fatalf("caller without Page access was told %q", reason)
		}
		w := httptest.NewRecorder()
		craftNativeAgentTaskError(w, err)
		var body map[string]any
		if json.Unmarshal(w.Body.Bytes(), &body) != nil || body["reason"] != nil || body["code"] != nil {
			t.Fatalf("refusal without Page access carried detail: %s", w.Body)
		}
		return w.Code, w.Body.String()
	}
	baseCode, baseBody := answer(t, "@native-agent-page", nil)
	for name, arrange := range map[string]func(*artifactPrincipalFixture, *craftNativeAgentManager){
		"policy disabled": func(f *artifactPrincipalFixture, _ *craftNativeAgentManager) {
			mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
		},
		"crew excluded": func(f *artifactPrincipalFixture, _ *craftNativeAgentManager) {
			mustExec(t, f.rig.db, `DELETE FROM workspace_artifact_creation_crews`)
		},
		"group chat": func(f *artifactPrincipalFixture, _ *craftNativeAgentManager) {
			mustExec(t, f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, f.a.ChatID)
		},
		"ordinary turn": func(f *artifactPrincipalFixture, _ *craftNativeAgentManager) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET native_craft_instruction=0 WHERE id=?`, f.a.MessageID)
		},
		"registry full": func(_ *artifactPrincipalFixture, m *craftNativeAgentManager) {
			m.mu.Lock()
			for i := range 128 {
				m.entries[fmt.Sprintf("filler-%d", i)] = &craftNativeAgentEntry{}
			}
			m.mu.Unlock()
		},
	} {
		t.Run(name, func(t *testing.T) {
			if code, body := answer(t, "@native-agent-page", arrange); code != baseCode || body != baseBody {
				t.Fatalf("answer depends on hidden state: %d %s, baseline %d %s", code, body, baseCode, baseBody)
			}
		})
	}
	t.Run("missing page", func(t *testing.T) {
		if code, body := answer(t, "@no-such-page", nil); code != baseCode || body != baseBody {
			t.Fatalf("a missing Page answers differently: %d %s, baseline %d %s", code, body, baseCode, baseBody)
		}
	})
}

// #5596 review F2: the owner/delegation check comes after the recorded
// instruction and policy checks, so a non-owner Page editor cannot probe an
// agent's grant through a chat that is not their own recorded instruction.
func TestCraftNativeAgentTaskRefusalChecksDelegationLast(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		arrange      func(t *testing.T, f *artifactPrincipalFixture)
	}{
		{"group chat with an ungranted agent", craftNativeAgentReasonChatNotPrivate, func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE chats SET visibility='group' WHERE id=?`, f.a.ChatID)
		}},
		{"ordinary turn with an ungranted agent", craftNativeAgentReasonInstructionNotRecorded, func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE conversation_messages SET native_craft_instruction=0 WHERE id=?`, f.a.MessageID)
		}},
		{"policy disabled with an ungranted agent", craftNativeAgentReasonPolicyDisabled, func(t *testing.T, f *artifactPrincipalFixture) {
			mustExec(t, f.rig.db, `UPDATE workspace_artifact_creation_policy SET enabled=0`)
		}},
		{"recorded instruction with an ungranted agent", craftNativeAgentReasonNotOwnerOrDelegated, func(*testing.T, *artifactPrincipalFixture) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			// A human Edit grantee who does not own the document; the agent
			// has no grant from the owner.
			mustExec(t, f.rig.db, `UPDATE pages SET owner_user_id=? WHERE id='native-agent-page'`, f.rig.adminID)
			mustExec(t, f.rig.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('native-agent-page','user',?,'write',?)`, f.a.HumanUserID, f.rig.adminID)
			tc.arrange(t, f)
			accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true, Mode: craftNativeAgentModeApply})
			if !errors.Is(err, errCraftNativeAgentDenied) || accepted.WorkID != "" {
				t.Fatalf("refusal lost the mandate sentinel: %+v %v", accepted, err)
			}
			if got := craftNativeAgentRefusalReason(err); got != tc.reason {
				t.Fatalf("reason = %q, want %q", got, tc.reason)
			}
		})
	}
}
