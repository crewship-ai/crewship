package api

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func TestPageArtifacts_WorkspaceQuotaSurvivesDeletionAndAllowsExactRetry(t *testing.T) {
	h, _, clock, workspace, user, page := artifactFixture(t)
	for i := 0; i < 199; i++ {
		if _, err := h.db.Exec(`INSERT INTO artifact_workspace_writes(id,workspace_id,created_at) VALUES(?,?,?)`, fmt.Sprintf("prior-%d", i), workspace, tsformat.Format(clock.Now())); err != nil {
			t.Fatal(err)
		}
	}
	artifactExpect(t, artifactSave(t, h, workspace, user, "<p>last allowed</p>", "last", 0), 201)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<p>last allowed</p>", "last", 0), 200)
	denied := artifactSave(t, h, workspace, user, "<p>limited</p>", "overflow", 1)
	artifactExpect(t, denied, 429)
	if !strings.Contains(denied.Body.String(), "workspace_rate_limited") || denied.Header().Get("Retry-After") == "" {
		t.Fatal("missing workspace rate indication")
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 200 {
		t.Fatalf("retry/denial consumed quota: count=%d err=%v", count, err)
	}
	if _, err := h.db.Exec(`DELETE FROM pages WHERE id=?`, page); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 200 {
		t.Fatalf("deletion reset quota: count=%d err=%v", count, err)
	}
	created := httptest.NewRecorder()
	h.Create(created, pagesGrantRequest(t, "POST", "/api/v1/pages", workspace, user, "OWNER", `{"slug":"report","name":"Replacement","panels":[],"allow_empty":true}`))
	artifactExpect(t, created, 201)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<p>new page</p>", "new", 0), 429)
	clock.advance(time.Hour + time.Second)
	artifactExpect(t, artifactSave(t, h, workspace, user, "<p>new page</p>", "new", 0), 201)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM artifact_workspace_writes WHERE workspace_id=?`, workspace).Scan(&count); err != nil || count != 1 {
		t.Fatalf("old receipts not compacted: count=%d err=%v", count, err)
	}
}

func TestArtifactUploadWaitIsBoundedByRequestContext(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	a, ok := acquireArtifactUpload(httptest.NewRecorder(), request)
	if !ok {
		t.Fatal("first slot")
	}
	defer a()
	b, ok := acquireArtifactUpload(httptest.NewRecorder(), request)
	if !ok {
		t.Fatal("second slot")
	}
	defer b()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := httptest.NewRecorder()
	if _, ok := acquireArtifactUpload(response, request.WithContext(ctx)); ok {
		t.Fatal("cancelled request admitted over capacity")
	}
	if response.Code != 408 {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestArtifactStreamDecoderRejectsTrailingAndUnknownData(t *testing.T) {
	for _, body := range []string{`{"html":"a"} {"html":"b"}`, `{"owner":"forged"}`, `{"html":`, `{"html":"a"} trailing`} {
		var value struct {
			HTML string `json:"html"`
		}
		response := httptest.NewRecorder()
		if decodeArtifactJSON(response, httptest.NewRequest("POST", "/", strings.NewReader(body)), &value) {
			t.Fatalf("accepted %q", body)
		}
		if response.Code != 400 {
			t.Fatalf("status=%d", response.Code)
		}
	}
}
