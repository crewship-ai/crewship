package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// #1669 — the Art. 17 cascade walked peer_cards, memory_versions and
// inbox_items and never touched user_models.
//
// Harmless for exactly as long as the extractor was a no-op and the file
// was always empty. It is not any more: the operator model is now the one
// place holding the person's own stated facts, and unlike the opt-out
// path there is no daily sweep behind an admin erase — SyncUserModel
// purges on user_peer_consent, which a SAR erase does not set. So the
// file survived the erase indefinitely and was read into every agent
// prompt afterwards, while the SAR ticket said "deleted".
func (r *gdprTestRig) seedUserModel(t *testing.T, content string) memory.UserModelPaths {
	t.Helper()
	paths := memory.UserModelPaths{
		SharedDir: filepath.Join(r.output, "crews", r.crewID, "shared", ".memory"),
	}
	if err := memory.WriteUserModel(paths, r.targetID, r.wsID, content); err != nil {
		t.Fatalf("seed disk model: %v", err)
	}
	slug := memory.UserSlug(r.targetID, r.wsID)
	if _, err := r.db.Exec(`INSERT INTO user_models
		(id, workspace_id, crew_id, user_id, user_slug, path, bytes)
		VALUES ('um1',?,?,?,?,?,?)`,
		r.wsID, r.crewID, r.targetID, slug, paths.ModelPath(slug), len(content)); err != nil {
		t.Fatalf("seed user_models: %v", err)
	}
	return paths
}

func TestGDPRCascade_DeletesTheOperatorModel(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)
	paths := r.seedUserModel(t, "- role: runs the platform team\n- timezone: UTC+1")

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"GDPR SAR ticket #1234"}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected 202/207; got %d body=%s", rec.Code, rec.Body.String())
	}

	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_models
		WHERE workspace_id=? AND user_id=?`, r.wsID, r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count user_models: %v", err)
	}
	if cnt != 0 {
		t.Errorf("user_models rows remaining for the subject = %d, want 0", cnt)
	}
	body, err := memory.LoadUserModel(paths, r.targetID, r.wsID)
	if err != nil {
		t.Fatalf("read model: %v", err)
	}
	if body != "" {
		t.Errorf("the operator model survived the erase on disk: %q", body)
	}

	// The action row's scope has to name it, or the compliance artefact
	// describes an erase that is narrower than the one performed.
	var scopeJSON string
	if err := r.db.QueryRow(
		`SELECT scope_json FROM gdpr_actions WHERE data_subject_id=? AND action='delete'`,
		r.targetID).Scan(&scopeJSON); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	if got, _ := scope["user_models"].(float64); got != 1 {
		t.Errorf("scope_json reports user_models=%v, want 1 (%s)", scope["user_models"], scopeJSON)
	}
}

// Art. 15 has the mirror obligation: an export that omits the operator
// model tells the subject less than is stored about them.
func TestGDPRExport_IncludesTheOperatorModel(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)
	r.seedUserModel(t, "- role: runs the platform team")

	rec := httptest.NewRecorder()
	r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, "ADMIN"))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	var bundle struct {
		UserModels []struct {
			ID       string `json:"id"`
			UserSlug string `json:"user_slug"`
			Bytes    int    `json:"bytes"`
			Content  string `json:"content"`
		} `json:"user_models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if len(bundle.UserModels) != 1 {
		t.Fatalf("export carries %d operator models, want 1: %s", len(bundle.UserModels), rec.Body.String())
	}
	// The CONTENT is the point of an access request — an index row
	// telling the subject that 30 bytes exist somewhere is not an answer
	// to "what do you hold about me".
	if bundle.UserModels[0].Content != "- role: runs the platform team" {
		t.Errorf("export omitted the model body: %+v", bundle.UserModels[0])
	}
}

// Same defect as TestDeleteMyUserModel_RemovesOrphanFromPriorCrew, but
// through the admin SAR erasure cascade: DeleteUserData resolves the
// on-disk path from the single (workspace_id, user_id) index row's
// crew_id, so a copy left behind in an earlier crew — after the writer's
// ON CONFLICT DO UPDATE moved crew_id forward without deleting it —
// survives an Art. 17 erasure indefinitely.
func TestGDPRCascade_RemovesOrphanFromPriorCrew(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)
	if _, err := r.db.Exec(`INSERT INTO crews (id, workspace_id, name, slug, network_mode, allowed_domains)
		VALUES ('crew2','ws1','C2','c2','free','[]')`); err != nil {
		t.Fatalf("seed crew2: %v", err)
	}

	oldPaths := memory.UserModelPaths{SharedDir: filepath.Join(r.output, "crews", "crew1", "shared", ".memory")}
	if err := memory.WriteUserModel(oldPaths, r.targetID, r.wsID, "- role: runs the platform team"); err != nil {
		t.Fatalf("seed old crew model: %v", err)
	}
	newPaths := memory.UserModelPaths{SharedDir: filepath.Join(r.output, "crews", "crew2", "shared", ".memory")}
	if err := memory.WriteUserModel(newPaths, r.targetID, r.wsID, "- role: runs the platform team\n- timezone: UTC+1"); err != nil {
		t.Fatalf("seed new crew model: %v", err)
	}
	slug := memory.UserSlug(r.targetID, r.wsID)
	if _, err := r.db.Exec(`INSERT INTO user_models
		(id, workspace_id, crew_id, user_id, user_slug, path, bytes)
		VALUES ('um1',?,?,?,?,?,40)`,
		r.wsID, "crew2", r.targetID, slug, newPaths.ModelPath(slug)); err != nil {
		t.Fatalf("seed user_models: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"GDPR SAR ticket #1234"}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected 202/207; got %d body=%s", rec.Code, rec.Body.String())
	}

	if body, _ := memory.LoadUserModel(oldPaths, r.targetID, r.wsID); body != "" {
		t.Errorf("crew1's orphaned copy survived the SAR erasure: %q", body)
	}
	if body, _ := memory.LoadUserModel(newPaths, r.targetID, r.wsID); body != "" {
		t.Errorf("crew2's copy survived the SAR erasure: %q", body)
	}
	var cnt int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_models
		WHERE workspace_id=? AND user_id=?`, r.wsID, r.targetID).Scan(&cnt); err != nil {
		t.Fatalf("count user_models: %v", err)
	}
	if cnt != 0 {
		t.Errorf("user_models rows remaining for the subject = %d, want 0", cnt)
	}
}

// Cross-subject isolation: erasing one person must not reach another's
// model, even in the same workspace and crew.
func TestGDPRCascade_LeavesOtherOperatorsModelsAlone(t *testing.T) {
	r := gdprTestSetup(t)
	paths := r.seedUserModel(t, "- role: runs the platform team")
	if err := memory.WriteUserModel(paths, "member1", r.wsID, "- role: runs billing"); err != nil {
		t.Fatalf("seed other model: %v", err)
	}
	slug := memory.UserSlug("member1", r.wsID)
	if _, err := r.db.Exec(`INSERT INTO user_models
		(id, workspace_id, crew_id, user_id, user_slug, path, bytes)
		VALUES ('um2',?,?,'member1',?,?,20)`,
		r.wsID, r.crewID, slug, paths.ModelPath(slug)); err != nil {
		t.Fatalf("seed other row: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"GDPR SAR ticket #1234"}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected 202/207; got %d", rec.Code)
	}

	body, _ := memory.LoadUserModel(paths, "member1", r.wsID)
	if body != "- role: runs billing" {
		t.Errorf("another operator's model was destroyed: %q", body)
	}
	var cnt int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE user_id='member1'`).Scan(&cnt)
	if cnt != 1 {
		t.Errorf("another operator's index row was destroyed (%d rows)", cnt)
	}
}
