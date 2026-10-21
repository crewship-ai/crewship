//go:build linux

package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

type publicationTestDBError int

func (e publicationTestDBError) Error() string { return "fixture SQLite error" }
func (e publicationTestDBError) Code() int     { return int(e) }

func TestPublicationWriteContentionStatus(t *testing.T) {
	for _, tc := range []struct {
		code, status int
	}{{5, 409}, {517, 409}, {6, 409}, {1, 500}} {
		t.Run(fmt.Sprint(tc.code), func(t *testing.T) {
			rr := httptest.NewRecorder()
			guestRoutineDraftError(rr, fmt.Errorf("write transaction: %w", publicationTestDBError(tc.code)))
			if rr.Code != tc.status {
				t.Errorf("SQLite write error %d: HTTP%d want%d", tc.code, rr.Code, tc.status)
			}
		})
	}
}

func TestPublicationRetainsNullableStoredDescription(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	// Native draft documents may retain the live recipe's nullable description.
	// The ordinary draft reader already normalizes this case for an editor.
	if _, err := f.db.Exec(`UPDATE pipeline_drafts SET document_json=json_set(document_json,'$.description',NULL) WHERE id=?`, f.draft.DraftID); err != nil {
		t.Fatal(err)
	}
	f.call(f.publisherToken, "POST", f.path+"/validate", f.request(), http.StatusOK)
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), http.StatusOK)
	var description sql.NullString
	if err := f.db.QueryRow(`SELECT description FROM pipelines WHERE id='publish-routine'`).Scan(&description); err != nil {
		t.Fatal(err)
	}
	if description.Valid {
		t.Fatal("publication changed a stored NULL description")
	}
	f.checkForeignKeys()
}

func TestPublicationExistingWebhookIsUnsupported(t *testing.T) {
	f := newServerPublicationHTTPFixture(t)
	// Use the native writer while the selected version is still legacy. This
	// is a sequential authoring check, with no trigger firing or runtime call.
	hook, err := pipeline.NewWebhookStore(f.db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID: f.workspace, Name: "Existing fixture hook", TargetPipelineID: "publish-routine",
		Enabled: true, RateLimitPerMin: 1,
	})
	if err != nil || hook == nil {
		t.Fatalf("native legacy webhook calibration: %v", err)
	}
	before := f.snapshot(t)
	rr := f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), http.StatusUnprocessableEntity)
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	message, _ := body["error"].(string)
	if !strings.Contains(strings.ToLower(message), "trigger") {
		t.Fatal("unsupported trigger refusal does not explain the cause")
	}
	if after := f.snapshot(t); after != before {
		t.Fatal("refused publication changed recipe, history, draft or audit")
	}
	f.checkForeignKeys()
}
