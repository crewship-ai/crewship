package api

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactsDeliveryRegistersArtifactsWithoutBoardSurface(t *testing.T) {
	router, err := NewRouter(setupTestDB(t), "artifacts-delivery-synthetic-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{
		"GET /api/v1/pages/report/board", "POST /api/v1/pages/report/board",
		"POST /api/v1/pages/report/board/batches", "GET /api/v1/pages/report/board/requests",
		"GET /api/v1/internal/boards/report", "GET /api/v1/internal/board-requests/job",
		"GET /api/v1/pages/runtime/board-converter",
	} {
		method, path, _ := strings.Cut(route, " ")
		_, pattern := router.mux.Handler(httptest.NewRequest(method, path, nil))
		if pattern != "" {
			t.Errorf("unexpected Board route %s: %s", route, pattern)
		}
	}
	for _, route := range []string{"GET /api/v1/pages/report/artifact", "POST /api/v1/pages/report/artifact/threads", "POST /api/v1/pages/report/artifact/requests"} {
		method, path, _ := strings.Cut(route, " ")
		_, pattern := router.mux.Handler(httptest.NewRequest(method, path, nil))
		if pattern == "" {
			t.Errorf("missing Artifact route %s", route)
		}
	}
}

func TestArtifactsDeliveryRejectsLegacyBoardCreationAuthority(t *testing.T) {
	h, workspace, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, workspace, user, "POST"), 201)
	// A previously admitted Board job can survive a downgrade. Its durable
	// authorization must not become a standalone Artifact creation capability.
	execOrFatal(t, h.db, `INSERT INTO page_board_requests(id,workspace_id,original_board_id,human_user_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) VALUES('legacy-board-job',?,'legacy-board',?,'artifact',0,'legacy-job','digest','instruction','[]','[]','{}','{}','','','','running','2026-10-13T00:00:00Z','2026-10-13T00:00:00Z')`, workspace, user)
	execOrFatal(t, h.db, `UPDATE artifact_creation_authorizations SET board_request_id='legacy-board-job' WHERE run_id='artifact-run'`)
	artifactExpect(t, artifactCreateAgent(t, h, workspace, token, creationBody), 403)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pages WHERE slug='agent-created'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("Board authority created Artifact: %d %v", count, err)
	}
}
