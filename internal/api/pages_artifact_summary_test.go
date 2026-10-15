package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPagesList_ArtifactSummaryIsCurrentMetadataWithoutBlobRead(t *testing.T) {
	h, _, _, ws, user, _ := artifactFixture(t)
	artifactExpect(t, artifactSave(t, h, ws, user, "<h1>Source must not enter listing</h1>", "summary-first", 0), 201)
	artifactExpect(t, artifactSave(t, h, ws, user, "<script>private source</script>", "summary-next", 1), 201)
	// A missing content store must not make the metadata gallery unreadable.
	h.artifactStoragePath = t.TempDir()
	rr := httptest.NewRecorder()
	h.List(rr, pagesGrantRequest(t, "GET", "/api/v1/pages", ws, user, "OWNER", ""))
	artifactExpect(t, rr, 200)
	var rows []pageListWire
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ArtifactSummary == nil {
		t.Fatalf("summary missing: %s", rr.Body.String())
	}
	summary := rows[0].ArtifactSummary
	if summary.HeadRev != 2 || summary.AuthorUserID != user || summary.PublishedAt == "" || summary.LastActivityAt == "" {
		t.Fatalf("wrong summary: %+v", summary)
	}
	if strings.Contains(rr.Body.String(), "private source") || strings.Contains(rr.Body.String(), "<h1>") {
		t.Fatal("list leaked source")
	}
	pagesSeedUser(t, h, ws, "stranger", "stranger@example.com", "MEMBER")
	denied := httptest.NewRecorder()
	h.List(denied, pagesGrantRequest(t, "GET", "/api/v1/pages", ws, "stranger", "MEMBER", ""))
	artifactExpect(t, denied, 200)
	if strings.Contains(denied.Body.String(), "artifact_summary") || strings.Contains(denied.Body.String(), "report") {
		t.Fatalf("unreachable metadata leaked: %s", denied.Body.String())
	}
}

func TestPagesList_ArtifactSummaryUsesAgentPublicationIdentity(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactAgentSave(t, h, ws, token, `{"html":"agent result","base_rev":0,"mutation_id":"summary-agent"}`), 201)
	rr := httptest.NewRecorder()
	h.List(rr, pagesGrantRequest(t, "GET", "/api/v1/pages", ws, user, "OWNER", ""))
	artifactExpect(t, rr, 200)
	var rows []pageListWire
	if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ArtifactSummary == nil || rows[0].ArtifactSummary.AuthorAgentID != "artifact-agent" || rows[0].ArtifactSummary.AuthorUserID != user {
		t.Fatalf("agent identity lost: %s", rr.Body.String())
	}
}
