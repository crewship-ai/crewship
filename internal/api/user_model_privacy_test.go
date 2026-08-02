package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// seedUserModel writes the disk file and the index row the way
// consolidate.SyncUserModel does.
func (r *peerTestRig) seedUserModel(t *testing.T, userID, content string) {
	t.Helper()
	paths := memory.UserModelPaths{
		SharedDir: filepath.Join(r.output, "crews", r.crewID, "shared", ".memory"),
	}
	if err := memory.WriteUserModel(paths, userID, r.wsID, content); err != nil {
		t.Fatalf("seed disk model: %v", err)
	}
	slug := memory.UserSlug(userID, r.wsID)
	if _, err := r.db.Exec(`
		INSERT INTO user_models (id, workspace_id, crew_id, user_id, user_slug, path, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "um-"+userID, r.wsID, r.crewID, userID, slug, paths.ModelPath(slug), len(content)); err != nil {
		t.Fatalf("seed db row: %v", err)
	}
}

func (r *peerTestRig) userModelOnDisk(t *testing.T, userID string) string {
	t.Helper()
	paths := memory.UserModelPaths{
		SharedDir: filepath.Join(r.output, "crews", r.crewID, "shared", ".memory"),
	}
	body, err := memory.LoadUserModel(paths, userID, r.wsID)
	if err != nil {
		t.Fatalf("read model: %v", err)
	}
	return body
}

// Opting out promises "every existing record about you is deleted
// immediately" — the handler doc says so and the CLI repeats it. It
// purged peer cards only. The operator model, which is the file that now
// actually contains what the person said about themselves, survived until
// the next daily sweep at 05:00 UTC and was read into every agent prompt
// in between.
//
// Latent while the extractor was a no-op (the file was always empty).
// Live the moment it is not.
func TestPutConsent_OptOutPurgesTheOperatorModelToo(t *testing.T) {
	r := peerTestSetup(t)
	r.seedCard(t, "u1", "peer card")
	r.seedUserModel(t, "u1", "- role: runs the platform team")

	rec := httptest.NewRecorder()
	r.privacy.PutConsent(rec, r.req(t, http.MethodPut, `{"opted_out":true}`, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("opt out: %d %s", rec.Code, rec.Body.String())
	}

	if body := r.userModelOnDisk(t, "u1"); body != "" {
		t.Errorf("operator model survived opt-out on disk: %q", body)
	}
	var rows int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE user_id='u1'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("user_models index row survived opt-out (%d rows)", rows)
	}
}

// A person must be able to read the model kept about them. The schema's
// only escape was "turn it all off"; that is not the same as seeing it.
func TestGetMyUserModel_ReturnsTheStoredFacts(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1")

	rec := httptest.NewRecorder()
	r.privacy.GetMyUserModel(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Content string `json:"content"`
		Facts   []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"facts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.Contains(got.Content, "runs the platform team") {
		t.Errorf("content missing the stored fact: %q", got.Content)
	}
	if len(got.Facts) != 2 || got.Facts[0].Key != "role" || got.Facts[1].Value != "UTC+1" {
		t.Errorf("facts not parsed per field: %+v", got.Facts)
	}
	// Reading your own record is auditable, same as reading a peer card.
	var reads int
	if err := r.db.QueryRow(
		`SELECT COUNT(*) FROM peer_card_audit WHERE action='read' AND target_user_id='u1'`).Scan(&reads); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if reads != 1 {
		t.Errorf("expected 1 read audit row; got %d", reads)
	}
}

// "This one is wrong" has to be answerable without deleting everything.
func TestForgetUserModelFact_RemovesOneFieldAndKeepsTheRest(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1\n- language: Czech")

	rec := httptest.NewRecorder()
	r.privacy.ForgetUserModelFact(rec, r.req(t, http.MethodDelete, "", map[string]string{"key": "timezone"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body.String())
	}

	body := r.userModelOnDisk(t, "u1")
	if strings.Contains(body, "timezone") {
		t.Errorf("the forgotten field is still stored: %q", body)
	}
	for _, keep := range []string{"role: runs the platform team", "language: Czech"} {
		if !strings.Contains(body, keep) {
			t.Errorf("forgetting one field dropped another (%q): %q", keep, body)
		}
	}
	// The index row's byte count has to follow the file, or a later sweep
	// reconciles against a size that was never on disk.
	var bytesCol int
	if err := r.db.QueryRow(`SELECT bytes FROM user_models WHERE user_id='u1'`).Scan(&bytesCol); err != nil {
		t.Fatalf("read index: %v", err)
	}
	if bytesCol != len(body) {
		t.Errorf("index says %d bytes, file is %d", bytesCol, len(body))
	}
}

// Forgetting the LAST field leaves no file — an empty model is not a
// model, and memory.WriteUserModel rejects empty content anyway.
func TestForgetUserModelFact_LastFieldRemovesTheModel(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team")

	rec := httptest.NewRecorder()
	r.privacy.ForgetUserModelFact(rec, r.req(t, http.MethodDelete, "", map[string]string{"key": "role"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("forget: %d %s", rec.Code, rec.Body.String())
	}
	if body := r.userModelOnDisk(t, "u1"); body != "" {
		t.Errorf("model file survived losing its last field: %q", body)
	}
	var rows int
	_ = r.db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE user_id='u1'`).Scan(&rows)
	if rows != 0 {
		t.Errorf("index row survived losing its last field (%d)", rows)
	}
}

func TestForgetUserModelFact_UnknownKeyIs404(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team")

	rec := httptest.NewRecorder()
	r.privacy.ForgetUserModelFact(rec, r.req(t, http.MethodDelete, "", map[string]string{"key": "timezone"}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 for a field that is not stored; got %d %s", rec.Code, rec.Body.String())
	}
	if body := r.userModelOnDisk(t, "u1"); !strings.Contains(body, "role") {
		t.Errorf("a miss must not touch the file: %q", body)
	}
}

func TestDeleteMyUserModel_RemovesEverything(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1")

	rec := httptest.NewRecorder()
	r.privacy.DeleteMyUserModel(rec, r.req(t, http.MethodDelete, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if body := r.userModelOnDisk(t, "u1"); body != "" {
		t.Errorf("model survived a delete: %q", body)
	}
}

// Cross-operator isolation: acting on your own model must never reach
// somebody else's, even in the same workspace and the same crew.
func TestUserModelPrivacy_NeverTouchesAnotherOperator(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team")
	r.seedUserModel(t, "u2", "- role: runs billing")

	rec := httptest.NewRecorder()
	r.privacy.DeleteMyUserModel(rec, r.req(t, http.MethodDelete, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if body := r.userModelOnDisk(t, "u2"); !strings.Contains(body, "runs billing") {
		t.Errorf("another operator's model was destroyed: %q", body)
	}
}
