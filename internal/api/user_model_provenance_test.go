package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
)

// #1693 — the read says where each fact came from, and every one of the
// four delete paths takes the evidence with the model:
//
//  1. opt-out            PutConsent → purgeUserModel
//  2. self-service delete DeleteMyUserModel → purgeUserModel
//  3. per-field forget   ForgetUserModelFact (one key; last field → purge)
//  4. Art. 17 cascade    AdminGDPRHandler.DeleteUserData (identity step)
//
// The sweep's own opt-out purge is the consolidate package's test.

// seedUserModelProvenance plants evidence rows the way the sweep does,
// keyed on the slug the file carries.
func seedUserModelProvenance(t *testing.T, r *peerTestRig, userID string, rows ...[3]string) {
	t.Helper()
	slug := memory.UserSlug(userID, r.wsID)
	for i, row := range rows {
		if _, err := r.db.Exec(`INSERT INTO user_model_provenance
			(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'stated', ?)`,
			"ump-"+userID+"-"+row[0]+"-"+string(rune('a'+i)), r.wsID, userID, slug,
			row[0], "value of "+row[0], row[1], row[2],
			// Later rows are newer, so the read has a definite answer.
			"2026-09-0"+string(rune('1'+i))+"T00:00:00.000Z"); err != nil {
			t.Fatalf("seed provenance: %v", err)
		}
	}
}

func provenanceRowsFor(t *testing.T, r *peerTestRig, userID string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_model_provenance WHERE workspace_id = ? AND user_id = ?`,
		r.wsID, userID).Scan(&n); err != nil {
		t.Fatalf("count provenance: %v", err)
	}
	return n
}

type userModelReadBody struct {
	Facts []struct {
		Key        string `json:"key"`
		Value      string `json:"value"`
		Provenance *struct {
			Quote      string `json:"quote"`
			MessageID  string `json:"message_id"`
			SourceType string `json:"source_type"`
			At         string `json:"at"`
		} `json:"provenance"`
	} `json:"facts"`
}

// "When did I say that?" now has an answer: each fact carries the newest
// evidence row for its key, and a fact with no row carries nothing rather
// than something made up.
func TestGetMyUserModel_ReturnsProvenancePerFact(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1\n- language: Czech")
	seedUserModelProvenance(t, r, "u1",
		[3]string{"role", "I run the platform team here", "msg-1"},
		[3]string{"role", "I lead the platform team now", "msg-9"}, // newer: wins
		[3]string{"timezone", "I'm on UTC+1", "msg-2"},
	)

	rec := httptest.NewRecorder()
	r.privacy.GetMyUserModel(rec, r.req(t, http.MethodGet, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	var got userModelReadBody
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Facts) != 3 {
		t.Fatalf("facts = %d, want 3", len(got.Facts))
	}
	byKey := map[string]int{}
	for i, f := range got.Facts {
		byKey[f.Key] = i
	}
	role := got.Facts[byKey["role"]]
	if role.Provenance == nil {
		t.Fatalf("role has no provenance: %s", rec.Body.String())
	}
	if role.Provenance.Quote != "I lead the platform team now" || role.Provenance.MessageID != "msg-9" ||
		role.Provenance.SourceType != "stated" || role.Provenance.At != "2026-09-02T00:00:00.000Z" {
		t.Errorf("role provenance = %+v, want the newest row", role.Provenance)
	}
	if tz := got.Facts[byKey["timezone"]]; tz.Provenance == nil || tz.Provenance.MessageID != "msg-2" {
		t.Errorf("timezone provenance = %+v", tz.Provenance)
	}
	if lang := got.Facts[byKey["language"]]; lang.Provenance != nil {
		t.Errorf("a fact with no evidence row was given provenance: %+v", lang.Provenance)
	}
}

// Delete path 1 of 4.
func TestPutConsent_OptOutPurgesProvenance(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team")
	seedUserModelProvenance(t, r, "u1", [3]string{"role", "I run the platform team", "msg-1"})
	r.seedUserModel(t, "u2", "- role: writes the docs")
	seedUserModelProvenance(t, r, "u2", [3]string{"role", "I write the docs", "msg-2"})

	rec := httptest.NewRecorder()
	r.privacy.PutConsent(rec, r.req(t, http.MethodPut, `{"opted_out":true}`, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("opt out: %d %s", rec.Code, rec.Body.String())
	}
	if n := provenanceRowsFor(t, r, "u1"); n != 0 {
		t.Errorf("%d provenance row(s) survived opt-out", n)
	}
	if n := provenanceRowsFor(t, r, "u2"); n != 1 {
		t.Errorf("another operator's provenance was purged (%d left, want 1)", n)
	}
}

// Delete path 2 of 4.
func TestDeleteMyUserModel_PurgesProvenance(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1")
	seedUserModelProvenance(t, r, "u1",
		[3]string{"role", "I run the platform team", "msg-1"},
		[3]string{"timezone", "I'm on UTC+1", "msg-2"})
	r.seedUserModel(t, "u2", "- role: writes the docs")
	seedUserModelProvenance(t, r, "u2", [3]string{"role", "I write the docs", "msg-3"})

	rec := httptest.NewRecorder()
	r.privacy.DeleteMyUserModel(rec, r.req(t, http.MethodDelete, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if n := provenanceRowsFor(t, r, "u1"); n != 0 {
		t.Errorf("%d provenance row(s) survived the self-service delete", n)
	}
	if n := provenanceRowsFor(t, r, "u2"); n != 1 {
		t.Errorf("another operator's provenance was purged (%d left, want 1)", n)
	}
}

// Evidence with no index row behind it — a sync whose model has since
// gone by some other route — is still about the person. The delete
// reaches it even when there is no model to delete.
func TestDeleteMyUserModel_PurgesOrphanedProvenance(t *testing.T) {
	r := peerTestSetup(t)
	seedUserModelProvenance(t, r, "u1", [3]string{"role", "I run the platform team", "msg-1"})

	rec := httptest.NewRecorder()
	r.privacy.DeleteMyUserModel(rec, r.req(t, http.MethodDelete, "", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	if n := provenanceRowsFor(t, r, "u1"); n != 0 {
		t.Errorf("%d orphaned provenance row(s) survived", n)
	}
}

// Delete path 3 of 4: one field's rows go — all of them, not the newest
// only — and the other fields' rows stay.
func TestForgetUserModelFact_PurgesTheFieldsProvenance(t *testing.T) {
	r := peerTestSetup(t)
	r.seedUserModel(t, "u1", "- role: runs the platform team\n- timezone: UTC+1")
	seedUserModelProvenance(t, r, "u1",
		[3]string{"timezone", "I'm on UTC+1", "msg-1"},
		[3]string{"timezone", "UTC+1 these days", "msg-5"},
		[3]string{"role", "I run the platform team", "msg-2"})

	cases := []struct {
		name       string
		forget     string
		wantStatus int
		wantLeft   map[string]int
	}{
		{"one field, both of its rows", "timezone", http.StatusOK, map[string]int{"timezone": 0, "role": 1}},
		{"case-insensitive like the field itself", "ROLE", http.StatusOK, map[string]int{"timezone": 0, "role": 0}},
		{"a field that is not stored touches nothing", "language", http.StatusNotFound, map[string]int{"timezone": 0, "role": 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			r.privacy.ForgetUserModelFact(rec, r.req(t, http.MethodDelete, "", map[string]string{"key": tc.forget}))
			if rec.Code != tc.wantStatus {
				t.Fatalf("forget %q: %d %s", tc.forget, rec.Code, rec.Body.String())
			}
			for key, want := range tc.wantLeft {
				var n int
				if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_model_provenance WHERE workspace_id = ? AND user_id = ? AND key = ?`,
					r.wsID, "u1", key).Scan(&n); err != nil {
					t.Fatalf("count: %v", err)
				}
				if n != want {
					t.Errorf("provenance rows for %q after forgetting %q = %d, want %d", key, tc.forget, n, want)
				}
			}
		})
	}
	// The last forget above removed the last field, which takes the
	// purge path — and the file and index are gone with the evidence.
	var rows int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_models WHERE user_id = 'u1'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("user_models row survived forgetting the last field")
	}
}

// Delete path 4 of 4: the Art. 17 cascade. The sweep in
// admin_gdpr_pages_identity_test.go proves the table is reached at all;
// this proves the model's OWN evidence goes with the model in the rig the
// other user_models cascade tests use, and that the receipt names it.
func TestGDPRCascade_PurgesTheOperatorModelProvenance(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)
	r.seedUserModel(t, "- role: runs the platform team")
	slug := memory.UserSlug(r.targetID, r.wsID)
	for i, row := range [][2]string{{"I run the platform team", "msg-1"}, {"I lead the platform team", "msg-2"}} {
		if _, err := r.db.Exec(`INSERT INTO user_model_provenance
			(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type)
			VALUES (?, ?, ?, ?, 'role', 'v', ?, ?, 'stated')`,
			"ump-"+string(rune('a'+i)), r.wsID, r.targetID, slug, row[0], row[1]); err != nil {
			t.Fatalf("seed provenance: %v", err)
		}
	}
	// A bystander in the same workspace, whose evidence must survive.
	if _, err := r.db.Exec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type)
		VALUES ('ump-other', ?, 'member1', ?, 'role', 'v', 'I write the docs', 'msg-3', 'stated')`,
		r.wsID, memory.UserSlug("member1", r.wsID)); err != nil {
		t.Fatalf("seed bystander: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.DeleteUserData(rec, r.adminReq(t, http.MethodDelete,
		`{"reason":"GDPR SAR ticket #1693"}`, r.targetID, "ADMIN"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202; got %d body=%s", rec.Code, rec.Body.String())
	}

	count := func(userID string) int {
		t.Helper()
		var n int
		if err := r.db.QueryRow(`SELECT COUNT(*) FROM user_model_provenance WHERE workspace_id = ? AND user_id = ?`,
			r.wsID, userID).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}
	if n := count(r.targetID); n != 0 {
		t.Errorf("%d provenance row(s) survived the Art. 17 cascade", n)
	}
	if n := count("member1"); n != 1 {
		t.Errorf("the bystander's provenance was erased (%d left, want 1)", n)
	}

	var scopeJSON string
	if err := r.db.QueryRow(`SELECT scope_json FROM gdpr_actions WHERE data_subject_id=? AND action='delete'`,
		r.targetID).Scan(&scopeJSON); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	if got, _ := scope["user_model_provenance_removed"].(float64); got != 2 {
		t.Errorf("scope_json reports user_model_provenance_removed=%v, want 2 (%s)", scope["user_model_provenance_removed"], scopeJSON)
	}
}

// Art. 15 has the mirror obligation: an export that omits the evidence
// tells the subject less than is stored about them. Every row, newest
// first, whole — and the scope counts it.
func TestGDPRExport_IncludesTheOperatorModelProvenance(t *testing.T) {
	r := gdprTestSetup(t)
	r.seedAll(t)
	r.seedUserModel(t, "- role: runs the platform team")
	slug := memory.UserSlug(r.targetID, r.wsID)
	for i, row := range [][3]string{
		{"I run the platform team", "msg-1", "2026-09-14T05:00:00.000Z"},
		{"I lead the platform team", "msg-2", "2026-09-15T05:00:00.000Z"},
	} {
		if _, err := r.db.Exec(`INSERT INTO user_model_provenance
			(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type, recorded_at)
			VALUES (?, ?, ?, ?, 'role', 'runs the platform team', ?, ?, 'stated', ?)`,
			"ump-"+string(rune('a'+i)), r.wsID, r.targetID, slug, row[0], row[1], row[2]); err != nil {
			t.Fatalf("seed provenance: %v", err)
		}
	}
	if _, err := r.db.Exec(`INSERT INTO user_model_provenance
		(id, workspace_id, user_id, user_slug, key, value, quote, message_id, source_type)
		VALUES ('ump-other', ?, 'member1', ?, 'role', 'v', 'I write the docs', 'msg-3', 'stated')`,
		r.wsID, memory.UserSlug("member1", r.wsID)); err != nil {
		t.Fatalf("seed bystander: %v", err)
	}

	rec := httptest.NewRecorder()
	r.h.ExportUserData(rec, r.adminReq(t, http.MethodGet, "", r.targetID, "ADMIN"))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	var bundle struct {
		Provenance []struct {
			Key        string `json:"key"`
			Quote      string `json:"quote"`
			MessageID  string `json:"message_id"`
			SourceType string `json:"source_type"`
			RecordedAt string `json:"recorded_at"`
		} `json:"user_model_provenance"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatalf("decode bundle: %v", err)
	}
	if len(bundle.Provenance) != 2 {
		t.Fatalf("export carries %d provenance rows, want the subject's 2 and not the bystander's: %s", len(bundle.Provenance), rec.Body.String())
	}
	if bundle.Provenance[0].MessageID != "msg-2" || bundle.Provenance[1].MessageID != "msg-1" {
		t.Errorf("rows are not newest first: %+v", bundle.Provenance)
	}
	if bundle.Provenance[0].Quote != "I lead the platform team" || bundle.Provenance[0].SourceType != "stated" {
		t.Errorf("row lost its content: %+v", bundle.Provenance[0])
	}

	var scopeJSON string
	if err := r.db.QueryRow(`SELECT scope_json FROM gdpr_actions WHERE data_subject_id=? AND action='export'`,
		r.targetID).Scan(&scopeJSON); err != nil {
		t.Fatalf("read scope: %v", err)
	}
	var scope map[string]any
	if err := json.Unmarshal([]byte(scopeJSON), &scope); err != nil {
		t.Fatalf("decode scope: %v", err)
	}
	if got, _ := scope["user_model_provenance"].(float64); got != 2 {
		t.Errorf("scope_json reports user_model_provenance=%v, want 2 (%s)", scope["user_model_provenance"], scopeJSON)
	}
}
