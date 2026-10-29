package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/crewship-ai/crewship/internal/artifacts"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth/internaltoken"
)

func TestArtifactSlidesSignedRunReadAndExportExactOriginalAuthority(t *testing.T) {
	for _, finalConsent := range []bool{false, true} {
		t.Run(fmt.Sprintf("finalConsent=%v", finalConsent), func(t *testing.T) {
			t.Setenv("CREWSHIP_ARTIFACT_PROJECTS_ENABLED", "true")
			t.Setenv("CREWSHIP_ARTIFACT_SLIDES_ENABLED", "true")
			f := artifactProjectOriginalRunFixture(t)
			f.admit(t)
			f.pages.artifactMasterToken = "synthetic-dashboard-data-master"
			humanToken := mintTokenFor(t, f.rig.db, f.a.HumanUserID, "dashboard-consent")
			human := pagesRequest(t, "POST", "/api/v1/pages/artifacts/consent", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", `{"run_id":"principal-run"}`)
			human.Header.Set("Authorization", "Bearer "+humanToken)
			human = human.WithContext(context.WithValue(human.Context(), ctxAuthKind, AuthKindCLIToken))
			w := httptest.NewRecorder()
			f.pages.GrantArtifactProjectRunConsent(w, human)
			if w.Code != 200 {
				t.Fatalf("human consent: %d %s", w.Code, w.Body)
			}
			token := internaltoken.DeriveAgentRunToken(internaltoken.DeriveAgentRunKey(f.pages.artifactMasterToken, f.a.WorkspaceID, f.a.CrewID), f.a.WorkspaceID, f.a.AgentID, f.a.RunID)
			tool := func(operation, slug, payload string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", strings.NewReader(fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":%s}`, operation, slug, payload)))
				r.Header.Set("Authorization", "Bearer "+token)
				ctx := context.WithValue(r.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
				ctx = context.WithValue(ctx, ctxInternalTokenCrew, f.a.CrewID)
				got := httptest.NewRecorder()
				f.pages.ArtifactProjectTool(got, r.WithContext(ctx))
				return got
			}
			create := `{"name":"Dashboard",` + strings.TrimPrefix(slidesArtifactBody(0, "Žluťoučký kůň"), "{")
			if w := tool("artifact_create", "agent-slides", create); w.Code != 201 {
				t.Fatalf("run create: %d %s", w.Code, w.Body)
			}
			mustExec(t, f.rig.db, `INSERT INTO artifact_project_message_intents(message_id,workspace_id,human_user_id,chat_id,submission_id,base_version,run_id,created_at) VALUES(?,?,?,?,?,0,?,datetime('now'))`, f.a.MessageID, f.a.WorkspaceID, f.a.HumanUserID, f.a.ChatID, "dashboard-original", f.a.RunID)
			otherBody := `{"slug":"other-slides-page","name":"Other Slides",` + strings.TrimPrefix(slidesArtifactBody(0, "Other authorized Slides"), "{")
			other := pagesRequest(t, "POST", "/api/v1/pages/artifacts", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", otherBody)
			other.Header.Set("Authorization", "Bearer "+humanToken)
			other = other.WithContext(context.WithValue(other.Context(), ctxAuthKind, AuthKindCLIToken))
			otherCreated := httptest.NewRecorder()
			f.pages.CreateArtifactProject(otherCreated, other)
			if otherCreated.Code != 201 {
				t.Fatalf("second valid Page calibration: %d %s", otherCreated.Code, otherCreated.Body)
			}
			var otherPage string
			if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='other-slides-page'`).Scan(&otherPage); err != nil {
				t.Fatal(err)
			}
			otherRead := httptest.NewRecorder()
			f.pages.GetArtifactSlides(otherRead, slidesAPIRequest(t, f.pages, f.a.WorkspaceID, f.a.HumanUserID, otherPage, "slides?version=1"))
			if otherRead.Code != 200 {
				t.Fatalf("second valid projection calibration: %d %s", otherRead.Code, otherRead.Body)
			}
			var page string
			if err := f.rig.db.QueryRow(`SELECT id FROM pages WHERE slug='agent-slides'`).Scan(&page); err != nil {
				t.Fatal(err)
			}
			for _, op := range []string{"artifact_slides_read", "artifact_export_pdf"} {
				w = tool(op, "@"+page, `{"version":1}`)
				if w.Code != 200 {
					t.Fatalf("signed %s: %d %s", op, w.Code, w.Body)
				}
				if op == "artifact_export_pdf" {
					var receipt artifacts.ArtifactSlidesPDFResponse
					if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
						t.Fatal(err)
					}
					pdf, err := base64.StdEncoding.DecodeString(receipt.Data)
					sum := sha256.Sum256(pdf)
					if err != nil || receipt.CodeVersion != 1 || receipt.ByteLength != len(pdf) || receipt.SHA256 != hex.EncodeToString(sum[:]) || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
						t.Fatal("invalid PDF transport receipt")
					}
				}
				if got := tool(op, "@"+otherPage, `{"version":1}`); got.Code != 404 {
					t.Fatalf("escaped %s: %d %s", op, got.Code, got.Body)
				}
				if got := tool(op, "@"+page, `{"version":1}`+strings.Repeat(" ", 1<<20)); got.Code != 413 {
					t.Fatalf("Slides request limit expanded %s: %d", op, got.Code)
				}
				if got := tool(op, "agent-slides", `{"version":1}`); got.Code != 400 {
					t.Fatalf("mutable Page reference %s: %d", op, got.Code)
				}
				if got := tool(op, "@"+page, `{"version":1,"out":"/tmp/host.pdf"}`); got.Code != 400 {
					t.Fatalf("host path accepted %s: %d", op, got.Code)
				}
				forged := pagesRequest(t, "POST", "/api/v1/internal/pages/artifact-tool", f.a.WorkspaceID, f.a.HumanUserID, "OWNER", fmt.Sprintf(`{"operation":%q,"slug":%q,"payload":{"version":1}}`, op, "@"+page))
				got := httptest.NewRecorder()
				f.pages.ArtifactProjectTool(got, forged)
				if got.Code != 403 {
					t.Fatalf("human impersonation %s: %d", op, got.Code)
				}
			}
			if !finalConsent {
				mustExec(t, f.rig.db, `UPDATE users SET access_revoked_at='2026-10-28T00:00:00Z' WHERE id=?`, f.a.HumanUserID)
				for _, op := range []string{"artifact_slides_read", "artifact_export_pdf"} {
					if got := tool(op, "@"+page, `{"version":1}`); got.Code != 403 {
						t.Fatalf("revoked original human %s: %d %s", op, got.Code, got.Body)
					}
				}

				return
			}
			// Exercise the same signed-run principal through the final render fence,
			// after both bytes and their typed transport envelope already exist.
			signed := httptest.NewRequest("POST", "/api/v1/internal/pages/artifact-tool", nil)
			signed.Header.Set("Authorization", "Bearer "+token)
			signedCtx := context.WithValue(signed.Context(), ctxInternalTokenWS, f.a.WorkspaceID)
			signedCtx = context.WithValue(signedCtx, ctxInternalTokenCrew, f.a.CrewID)
			checked, ok := f.pages.artifactAgentRequest(httptest.NewRecorder(), signed.WithContext(signedCtx))
			if !ok {
				t.Fatal("signed principal calibration failed")
			}
			checked.SetPathValue("slug", "@"+page)
			checked.URL.RawQuery = "version=1"
			checked.Method = "GET"
			final := httptest.NewRecorder()
			processed := false
			f.pages.serveArtifactSlides(final, checked, false, func(ctx context.Context, p artifacts.ArtifactSlidesProjection) ([]byte, error) {
				processed = true
				encoded, err := artifactSlidesPDFProcess(1)(ctx, p)
				if err != nil {
					return nil, err
				}
				if len(encoded) == 0 || final.Body.Len() != 0 || final.Header().Get("Content-Type") != "" {
					t.Fatal("early encoded PDF delivery")
				}
				mustExec(t, f.rig.db, `DELETE FROM artifact_project_run_consents WHERE run_id=?`, f.a.RunID)
				return encoded, nil
			})
			if !processed || (final.Code != 403 && final.Code != 404) || strings.Contains(final.Body.String(), `"data"`) || strings.Contains(final.Body.String(), "JVBER") {
				t.Fatalf("revoked run consent escaped final fence: %d %s", final.Code, final.Body)
			}

		})
	}
}
