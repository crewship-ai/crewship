package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"
)

func boardPreviewPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, width, height))
	pixels.Set(0, 0, color.RGBA{R: 200, A: 255})
	var out bytes.Buffer
	if err := png.Encode(&out, pixels); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func boardRequestWithPreview(t *testing.T, base string, data []byte) string {
	t.Helper()
	var fields map[string]any
	if json.Unmarshal([]byte(base), &fields) != nil {
		t.Fatal("bad fixture")
	}
	fields["preview_png"] = data
	raw, _ := json.Marshal(fields)
	return string(raw)
}
func TestBoardRequestPreviewCanonicalBoundedPNG(t *testing.T) {
	input := boardPreviewPNG(t, 32, 32)
	out, width, height, err := canonicalBoardRequestPreview(append(input, []byte("TRAILING_PRIVATE_METADATA")...))
	if err != nil || width != 32 || height != 32 || bytes.Contains(out, []byte("TRAILING_PRIVATE_METADATA")) {
		t.Fatal("preview not canonicalized", err)
	}
	for _, bad := range [][]byte{nil, []byte("<svg/>"), bytes.Repeat([]byte("x"), maxBoardRequestPreviewInput+1), boardPreviewPNG(t, 1025, 1)} {
		if _, _, _, err := canonicalBoardRequestPreview(bad); err == nil {
			t.Fatal("unsafe/big preview accepted")
		}
	}
}
func TestBoardRequestPreviewAtomicRetryAndScopedRead(t *testing.T) {
	h, workspace, user, page, token, _ := boardJobFixture(t)
	body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "preview-request", 1), boardPreviewPNG(t, 20, 20))
	response := boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, response, 202)
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest), 202)
	var count int
	if h.db.QueryRow(`SELECT COUNT(*) FROM page_board_request_previews WHERE request_id=?`, result.ID).Scan(&count) != nil || count != 1 {
		t.Fatal("preview not atomically linked")
	}
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "preview-request", 1), boardPreviewPNG(t, 21, 20)), h.CreateBoardRequest), 409)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, result.ID, "", h.GetBoardRequestPreviewAgent), 403)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, result.ID)
	response = boardJobAgentCall(t, h, workspace, token, result.ID, "", h.GetBoardRequestPreviewAgent)
	artifactExpect(t, response, 200)
	if !strings.Contains(response.Body.String(), `"kind":"untrusted-human-illustration"`) || !strings.Contains(response.Body.String(), `"authority":"canonical-snapshot-only"`) {
		t.Fatal("missing authority distinction")
	}
	if _, err := h.db.Exec(`UPDATE page_board_request_previews SET width=21 WHERE request_id=?`, result.ID); err == nil {
		t.Fatal("helper mutable")
	}
	execOrFatal(t, h.db, `DELETE FROM pages WHERE id=?`, page)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, result.ID, "", h.GetBoardRequestPreviewAgent), 403)
	if h.db.QueryRow(`SELECT COUNT(*) FROM page_board_request_previews WHERE request_id=?`, result.ID).Scan(&count) != nil || count != 1 {
		t.Fatal("detached request lost retained helper")
	}
}
func TestBoardRequestPreviewRechecksEveryNormalizedSource(t *testing.T) {
	h, workspace, user, page, token, _ := boardJobFixture(t)
	execOrFatal(t, h.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('preview-source',?,'preview-source','Secret title',?,'{}')`, workspace, user)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at) VALUES('preview-source-rev','preview-source',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','source','digest','2020-01-01T00:00:00.000000000Z')`)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES('preview-source',1)`)
	execOrFatal(t, h.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('preview-source','agent','artifact-agent','read',?)`, user)
	execOrFatal(t, h.db, `INSERT INTO page_board_artifact_placements(id,board_page_id,source_page_id,source_revision,x,y,width,height,updated_seq) VALUES('preview-pin',?,'preview-source',1,0,0,100,100,0)`, page)
	base := strings.Replace(boardDiagramRequest, "diagram-request", "preview-pin-job", 1)
	base = strings.Replace(base, `"selected_placement_ids":[]`, `"selected_placement_ids":["preview-pin"]`, 1)
	body := boardRequestWithPreview(t, base, boardPreviewPNG(t, 20, 20))
	response := boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest)
	artifactExpect(t, response, 202)
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &result)
	execOrFatal(t, h.db, `UPDATE page_board_requests SET status='running',run_id='artifact-run' WHERE id=?`, result.ID)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, result.ID, "", h.GetBoardRequestPreviewAgent), 200)
	execOrFatal(t, h.db, `DELETE FROM page_grants WHERE page_id='preview-source'`)
	artifactExpect(t, boardJobAgentCall(t, h, workspace, token, result.ID, "", h.GetBoardRequestPreviewAgent), 403)
}
func TestBoardRequestPreviewQuotaDoesNotCreatePartialJob(t *testing.T) {
	h, workspace, user, _, _, id := boardJobFixture(t)
	for i := 0; i < 64; i++ {
		clone := fmt.Sprint("quota-preview-", i)
		execOrFatal(t, h.db, `INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) SELECT ?,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,?,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,'failed','2020-01-01T00:00:00Z',updated_at FROM page_board_requests WHERE id=?`, clone, clone, id)
		execOrFatal(t, h.db, `INSERT INTO page_board_request_previews(request_id,sha256,width,height,byte_size,created_at) VALUES(?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1024,1024,2097152,'2020-01-01T00:00:00Z')`, clone)
	}
	body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "over-preview-quota", 1), boardPreviewPNG(t, 20, 20))
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest), 409)
	var count int
	if h.db.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE submission_id='over-preview-quota'`).Scan(&count) != nil || count != 0 {
		t.Fatal("quota left partial queued job")
	}
}

func TestBoardPreviewQuotaReclaimsOnlyAgedTerminalIllustrations(t *testing.T) {
	h, workspace, user, _, _, id := boardJobFixture(t)
	old := tsformat.Format(time.Now().Add(-48 * time.Hour))
	for i := 0; i < 64; i++ {
		clone := fmt.Sprint("aged-quota-preview-", i)
		state := "failed"
		if i == 0 {
			state = "running"
		}
		execOrFatal(t, h.db, `INSERT INTO page_board_requests(id,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,submission_id,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,status,created_at,updated_at) SELECT ?,workspace_id,board_page_id,original_board_id,human_user_id,agent_id,chat_id,kind,base_seq,?,fingerprint,instruction,selected_element_ids_json,selected_placement_ids_json,snapshot_json,target_json,dialect,converter_version,receipt_json,?,?,? FROM page_board_requests WHERE id=?`, clone, clone, state, old, old, id)
		execOrFatal(t, h.db, `INSERT INTO page_board_request_previews(request_id,sha256,width,height,byte_size,created_at) VALUES(?,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,1,2097152,?)`, clone, old)
	}
	body := boardRequestWithPreview(t, strings.Replace(boardDiagramRequest, "diagram-request", "after-aged-preview-quota", 1), boardPreviewPNG(t, 20, 20))
	artifactExpect(t, boardJobCall(t, h, workspace, user, "", body, h.CreateBoardRequest), 202)
	var previews, history, active int
	if err := h.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(request_id='aged-quota-preview-0'),0) FROM page_board_request_previews`).Scan(&previews, &active); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_board_requests WHERE id LIKE 'aged-quota-preview-%'`).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if previews != 64 || active != 1 || history != 64 {
		t.Fatalf("previews=%d active=%d terminal history=%d", previews, active, history)
	}
}
