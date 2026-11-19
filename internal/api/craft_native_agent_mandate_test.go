package api

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Q16 (owner decision 2026-10-19): the author's targeted instruction is the
// mandate. Personal space: the agent changes the document directly. Anywhere
// shared: it proposes, unless the person explicitly chose apply.
func TestCraftNativeAgentModeDefaultsFollowLocation(t *testing.T) {
	for _, tc := range []struct {
		requested    string
		personal     bool
		mode         string
		explicitMode bool
	}{
		{"", true, craftNativeAgentModeApply, false},
		{"", false, craftNativeAgentModePropose, false},
		{"apply", false, craftNativeAgentModeApply, true},
		{"propose", true, craftNativeAgentModePropose, true},
	} {
		mode, explicit := craftNativeAgentMode(tc.requested, tc.personal)
		if mode != tc.mode || explicit != tc.explicitMode {
			t.Errorf("requested %q personal %v: got %s/%v", tc.requested, tc.personal, mode, explicit)
		}
	}
}

func TestCraftNativePersonalSpaceMatchesDocsRule(t *testing.T) {
	personal := &pageRecord{PrivateAudience: true, OwnerUserID: "u"}
	if !craftNativePersonalSpace(personal, "u") {
		t.Fatal("author's private unfiled page is personal space")
	}
	for name, rec := range map[string]*pageRecord{
		"shared page":           {OwnerUserID: "u"},
		"filed in a folder":     {PrivateAudience: true, OwnerUserID: "u", FolderID: "f"},
		"crew owned":            {PrivateAudience: true, OwnerUserID: "u", OwnerCrewID: "c"},
		"someone else's output": {PrivateAudience: true, OwnerUserID: "other"},
	} {
		if craftNativePersonalSpace(rec, "u") {
			t.Errorf("%s treated as personal space", name)
		}
	}
}

// The regression the opposition review named (D4): on a shared document an
// agent task without an explicit apply mandate is refused a direct write even
// when nobody holds the editor, and is told to propose instead.
func TestCraftNativeAgentSharedDocumentWithoutMandateMayOnlyPropose(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
	if err != nil {
		t.Fatal("admission", err)
	}
	m.mu.Lock()
	e := m.entries[accepted.WorkID]
	m.mu.Unlock()
	if e.grant.Mode != craftNativeAgentModePropose || e.grant.ModeExplicit {
		t.Fatalf("shared document default mode: %s explicit=%v", e.grant.Mode, e.grant.ModeExplicit)
	}
	if err = m.directMandate(t.Context(), e); !errors.Is(err, errCraftNativeAgentProposalRequired) || !errors.Is(err, errCraftNativeAgentDenied) {
		t.Fatalf("direct write without mandate: %v", err)
	}
	var input string
	if err = f.rig.db.QueryRow(`SELECT input_json FROM work_items WHERE id=?`, accepted.WorkID).Scan(&input); err != nil || !jsonHasMode(input, "propose", false) {
		t.Fatalf("work input does not record the mode: %s %v", input, err)
	}
}

func TestCraftNativeAgentExplicitApplyIsTheMandateOnSharedDocument(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true, Mode: craftNativeAgentModeApply})
	if err != nil {
		t.Fatal("admission", err)
	}
	m.mu.Lock()
	e := m.entries[accepted.WorkID]
	m.mu.Unlock()
	if err = m.directMandate(t.Context(), e); err != nil {
		t.Fatalf("explicit apply refused: %v", err)
	}
}

// Q15/Q16: an implicit direct mandate belongs to the personal space. Filing
// the document into a shared folder mid-run turns the rest into proposals.
func TestCraftNativeAgentImplicitApplyEndsWhenDocumentLeavesPersonalSpace(t *testing.T) {
	f, m, ctx := nativeAgentAdmissionFixture(t)
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='native-agent-page'`)
	accepted, err := m.produceTask(ctx, "@native-agent-page", craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
	if err != nil {
		t.Fatal("admission in personal space", err)
	}
	m.mu.Lock()
	e := m.entries[accepted.WorkID]
	m.mu.Unlock()
	if e.grant.Mode != craftNativeAgentModeApply || e.grant.ModeExplicit {
		t.Fatalf("personal space default mode: %s explicit=%v", e.grant.Mode, e.grant.ModeExplicit)
	}
	if err = m.directMandate(t.Context(), e); err != nil {
		t.Fatalf("personal space direct write refused: %v", err)
	}
	mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='native-agent-page'`)
	if err = m.directMandate(t.Context(), e); !errors.Is(err, errCraftNativeAgentProposalRequired) {
		t.Fatalf("direct write survived leaving the personal space: %v", err)
	}
}

func TestCraftNativeAgentPromptMatchesMode(t *testing.T) {
	propose := craftNativeAgentPrompt(craftNativeAgentGrant{Mode: craftNativeAgentModePropose, Instruction: "x"})
	apply := craftNativeAgentPrompt(craftNativeAgentGrant{Mode: craftNativeAgentModeApply, Instruction: "x"})
	if !containsAll(propose, "never call craft_apply", "craft_propose") || !containsAll(apply, "Acquire the lease", "proposal_required") {
		t.Fatalf("prompts do not follow the mode:\n%s\n%s", propose, apply)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

func jsonHasMode(raw, mode string, explicit bool) bool {
	var input craftNativeAgentWorkInput
	return json.Unmarshal([]byte(raw), &input) == nil && input.Mode == mode && input.ModeExplicit == explicit
}
