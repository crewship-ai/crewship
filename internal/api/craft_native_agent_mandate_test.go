package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

func nativeAgentVersion(t *testing.T, f *artifactPrincipalFixture) int64 {
	t.Helper()
	var v int64
	if err := f.rig.db.QueryRow(`SELECT COALESCE(MAX(version),0) FROM artifact_versions WHERE page_id='native-agent-page'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func requireProposalRequired(t *testing.T, what string, w *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Reason string `json:"reason"`
	}
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Reason != "proposal_required" {
		t.Fatalf("%s: %d %s", what, w.Code, w.Body)
	}
}

// D4 at the HTTP boundary: a shared document, a free lease, no explicit
// apply. The agent cannot take the editor or write, and can still propose.
func TestCraftNativeAgentToolSharedDocumentProposesOnly(t *testing.T) {
	f, _, _, call, calls := nativeAgentToolFixtureFor(t, "", nil)
	before := nativeAgentVersion(t, f)
	requireProposalRequired(t, "acquire", call(`{"operation":"acquire"}`))
	requireProposalRequired(t, "apply", call(`{"operation":"apply","generation":"1","base_version":"1","request_id":"direct","commands":[{"command":"text.insert","params":{"text":"x"}}]}`))
	requireProposalRequired(t, "renew", call(`{"operation":"renew","generation":"1"}`))
	if *calls != 0 || nativeAgentVersion(t, f) != before {
		t.Fatalf("refused direct write ran the engine (%d) or changed the document", *calls)
	}
	proposed := call(`{"operation":"propose","base_version":"1","request_id":"proposal","summary":"Review","commands":[{"command":"text.insert","params":{"text":"x"}}]}`)
	if proposed.Code != http.StatusCreated {
		t.Fatalf("propose in propose mode: %d %s", proposed.Code, proposed.Body)
	}
	if nativeAgentVersion(t, f) != before {
		t.Fatal("a proposal changed the document")
	}
}

// The document leaves the personal space after the pre-check, while the
// engine runs. Any change of privacy, folder or owner advances the page's
// authority epoch (mooring_relevant_page_* triggers), so the publishing
// transaction refuses the save: definitely, with the document unchanged and
// the run not left uncertain. The Q16 re-check in authorize sits behind that
// fence as a second line.
func TestCraftNativeAgentToolMoveDuringApplyIsRefusedAtPublish(t *testing.T) {
	f, m, _, call, _ := nativeAgentToolFixtureFor(t, "", func(f *artifactPrincipalFixture) {
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='native-agent-page'`)
	})
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal("acquire in personal space", acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	before := nativeAgentVersion(t, f)
	m.config.AgentApply = func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error) {
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=0 WHERE id='native-agent-page'`)
		return []byte(`{"body":[{"kind":"para","text":"agent edited"}]}`), nil
	}
	w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"moved","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation))
	if w.Code != http.StatusForbidden && w.Code != http.StatusConflict {
		t.Fatalf("apply after move was not refused: %d %s", w.Code, w.Body)
	}
	if nativeAgentVersion(t, f) != before {
		t.Fatal("publish after the move changed the document")
	}
	for _, e := range m.entries {
		if e.tool != nil && e.tool.uncertain {
			t.Fatal("a definite refusal marked the run uncertain")
		}
	}
}

// Q16 personal space, end to end through the tool route: without a mode the
// agent takes the editor and saves directly. On main this was unreachable,
// because the agent call loaded the private page without its author.
func TestCraftNativeAgentToolPersonalSpaceAppliesDirectly(t *testing.T) {
	f, _, _, call, calls := nativeAgentToolFixtureFor(t, "", func(f *artifactPrincipalFixture) {
		mustExec(t, f.rig.db, `UPDATE pages SET private_audience=1 WHERE id='native-agent-page'`)
	})
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal("acquire in personal space", acquired.Code, acquired.Body)
	}
	var lease struct {
		Generation string `json:"generation"`
	}
	json.Unmarshal(acquired.Body.Bytes(), &lease)
	before := nativeAgentVersion(t, f)
	w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"personal","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, lease.Generation))
	if w.Code != 200 || *calls != 1 || nativeAgentVersion(t, f) != before+1 {
		t.Fatalf("personal space direct apply: %d %s calls=%d", w.Code, w.Body, *calls)
	}
}
