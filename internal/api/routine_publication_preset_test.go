package api

import (
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
)

func TestPresetDowngradeCannotRetainPublishWithoutEdit(t *testing.T) {
	f := newPresetApplyFixture(t)
	seedPipelineRow(t, f.db, f.workspace, "publish-preset", "publish-preset")
	m, err := f.store.Membership(t.Context(), "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{
		{Kind: "routine", ID: "publish-preset", Operation: "view"},
		{Kind: "routine", ID: "publish-preset", Operation: "edit"},
		{Kind: "routine", ID: "publish-preset", Operation: "publish"},
	}
	m, err = f.store.Replace(t.Context(), f.owner, "apply-subject", f.workspace, "restricted", m, rights)
	if err != nil {
		t.Fatal("positive explicit Edit+Publish calibration", err)
	}
	f.request = access.PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: access.PresetResource{Kind: "routine", ID: "publish-preset"}, PresetID: "routine-levels", Version: 1, Level: "view", Reissue: []access.Right{}}
	preview := f.preview(t)
	before, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	response := f.apply(t, applyBody(t, preview))
	if response.Code != 422 {
		t.Fatalf("preset retained Publish after removing Edit: HTTP%d", response.Code)
	}
	after, err := f.store.Policy(t.Context(), f.owner, "apply-subject", f.workspace)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("refused preset changed policy or revision", err)
	}
	for _, table := range []string{"journal_entries", "access_preset_selections"} {
		var count int
		if err := f.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id=?`, f.workspace).Scan(&count); err != nil || count != 0 {
			t.Fatalf("refused preset persisted %s: count=%d err=%v", table, count, err)
		}
	}
}
