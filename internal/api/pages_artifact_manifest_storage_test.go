package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

func requireArtifactManifestResponse(t *testing.T, raw []byte, status string, html string) {
	t.Helper()
	var wire struct {
		Manifest       *artifactManifest `json:"manifest"`
		ManifestStatus string            `json:"manifest_status"`
		HTML           string            `json:"html"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.ManifestStatus != status || wire.HTML != html {
		t.Fatalf("manifest response=%+v", wire)
	}
	if status == "unpublished" {
		if wire.Manifest != nil {
			t.Fatal("unpublished page invented manifest")
		}
		return
	}
	if wire.Manifest == nil || len(wire.Manifest.Files) != 1 || wire.Manifest.Files[0].SHA256 != attachmentDigest([]byte(html)) || wire.Manifest.Files[0].SizeBytes != int64(len(html)) {
		t.Fatalf("missing or incorrect manifest: %+v", wire.Manifest)
	}
}

func TestArtifactManifestUnsupportedPublishNeverWritesBlobOrRevision(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	empty := artifactCall(t, h, ws, user, "OWNER", "GET", "", "")
	artifactExpect(t, empty, 200)
	requireArtifactManifestResponse(t, empty.Body.Bytes(), "unpublished", "")
	const html = "<h1>Never stored unsupported profile</h1>"
	for i, format := range []string{`"runtime_profile":"interactive-html-v1"`, `"runtime_profile":null`, `"format_version":2`, `"format_version":null`} {
		body := fmt.Sprintf(`{"html":%q,"base_rev":0,"mutation_id":"unsupported-%d",%s}`, html, i, format)
		artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/revisions", body), 422)
	}
	path, err := attachmentBlobPath(h.artifactStoragePath, ws, attachmentDigest([]byte(html)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unsupported profile touched blobstorage: %v", err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE page_id=?`, page).Scan(&n); err != nil || n != 0 {
		t.Fatalf("unsupported created revision=%d %v", n, err)
	}
}

func TestArtifactManifestLegacyRetryRestoreCopyAndBackupFork(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	const html = "<h1>Legacy ž source</h1>"
	sha := attachmentDigest([]byte(html))
	if _, _, err := storeAttachmentBlob(h.artifactStoragePath, ws, []byte(html)); err != nil {
		t.Fatal(err)
	}
	fingerprint := attachmentDigest([]byte(fmt.Sprintf("%d/%t/%d/%s", 0, false, 0, sha)))
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at) VALUES('legacy-before-manifests',?,1,?,?,'legacy-mutation',?,'2026-10-13T00:00:00.000Z')`, page, sha, user, fingerprint)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, page)
	execOrFatal(t, h.db, `INSERT INTO page_artifact_mutation_receipts(page_id,mutation_id,revision,request_digest,author_user_id,created_at) VALUES(?,'legacy-mutation',1,?,?,'2026-10-13T00:00:00.000Z')`, page, fingerprint, user)
	get := artifactCall(t, h, ws, user, "OWNER", "GET", "", "")
	artifactExpect(t, get, 200)
	requireArtifactManifestResponse(t, get.Body.Bytes(), "legacy_derived", html)
	var recorded sql.NullString
	if err := h.db.QueryRow(`SELECT manifest_json FROM page_artifact_revisions WHERE page_id=? AND revision=1`, page).Scan(&recorded); err != nil || recorded.Valid {
		t.Fatalf("legacy read mutated row: %v %v", recorded, err)
	}
	retry, _ := json.Marshal(map[string]any{"html": html, "base_rev": 0, "mutation_id": "legacy-mutation", "runtime_profile": "static-html-v1", "format_version": 1})
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/revisions", string(retry)), 200)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":1,"base_rev":1,"mutation_id":"manifest-restore"}`), 201)
	body := `{"source_revision":1,"slug":"manifest-copy","name":"Manifest copy","mutation_id":"manifest-copy"}`
	req := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/copies", ws, user, "OWNER", body)
	req.SetPathValue("slug", "report")
	copyResponse := httptest.NewRecorder()
	h.CopyArtifact(copyResponse, req)
	artifactExpect(t, copyResponse, 201)
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE manifest_json IS NOT NULL`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("restore/copy did not record complete manifests: %d %v", count, err)
	}
	const pass = "artifact-manifest-roundtrip-password"
	made, err := backup.CreateBackup(t.Context(), h.db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: pass, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: h.artifactStoragePath})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []string{"", "manifest-fork"} {
		t.Run(fork, func(t *testing.T) {
			db := setupTestDB(t)
			root := storageDir(t)
			restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: made.Path, Passphrase: pass, AsWorkspace: fork, Actor: backup.Actor{UserID: user, Role: "ADMIN"}, AttachmentRoot: root})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewPageHandler(db, nil, newTestLogger())
			handler.artifactStoragePath = root
			for _, selected := range []struct{ query, status string }{{"?revision=1", "legacy_derived"}, {"?revision=2", "recorded"}} {
				rr := artifactCall(t, handler, restored.RestoredWorkspaceID, user, "OWNER", "GET", selected.query, "")
				artifactExpect(t, rr, 200)
				requireArtifactManifestResponse(t, rr.Body.Bytes(), selected.status, html)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions WHERE manifest_json IS NOT NULL`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("backup lost recorded manifests: %d %v", count, err)
			}
		})
	}
}

func TestArtifactManifestAgentCreationRecordsSupportedProfileOnly(t *testing.T) {
	h, ws, user, _, token := artifactRunFixture(t)
	artifactExpect(t, artifactCreationOptIn(t, h, ws, user, "POST"), 201)
	const html = "<h1>Agent manifest creation</h1>"
	unsupported, _ := json.Marshal(map[string]any{"slug": "agent-manifest", "name": "Agent manifest", "html": html, "mutation_id": "agent-manifest", "runtime_profile": "interactive-html-v1"})
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, string(unsupported)), 422)
	path, err := attachmentBlobPath(h.artifactStoragePath, ws, attachmentDigest([]byte(html)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unsupported agent profile touched blobstorage: %v", err)
	}
	supported, _ := json.Marshal(map[string]any{"slug": "agent-manifest", "name": "Agent manifest", "html": html, "mutation_id": "agent-manifest", "runtime_profile": "static-html-v1", "format_version": 1})
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, string(supported)), 201)
	var manifestJSON string
	if err := h.db.QueryRow(`SELECT v.manifest_json FROM page_artifact_revisions v JOIN pages p ON p.id=v.page_id WHERE p.slug='agent-manifest' AND v.revision=1`).Scan(&manifestJSON); err != nil {
		t.Fatal(err)
	}
	if _, status, err := resolveArtifactManifest(sql.NullString{String: manifestJSON, Valid: true}, []byte(html), attachmentDigest([]byte(html))); err != nil || status != "recorded" {
		t.Fatalf("agent manifest=%s status=%s %v", manifestJSON, status, err)
	}
	artifactExpect(t, artifactCreateAgent(t, h, ws, token, string(supported)), 200)
}

func TestArtifactManifestUnsupportedStoredProfileFailsClosed(t *testing.T) {
	h, _, _, ws, user, page := artifactFixture(t)
	const html = "<h1>Unsupported stored profile</h1>"
	sha := attachmentDigest([]byte(html))
	if _, _, err := storeAttachmentBlob(h.artifactStoragePath, ws, []byte(html)); err != nil {
		t.Fatal(err)
	}
	manifest, err := newArtifactManifest([]byte(html))
	if err != nil {
		t.Fatal(err)
	}
	manifest.RuntimeProfile = "interactive-html-v1"
	recorded, err := artifactManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) VALUES('unsupported-stored',?,1,?,?,'unsupported-stored','unsupported-stored','2026-10-13T00:00:00.000Z',?)`, page, sha, user, recorded)
	execOrFatal(t, h.db, `INSERT INTO page_artifacts(page_id,head_rev) VALUES(?,1)`, page)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "GET", "", ""), 503)
	artifactExpect(t, artifactCall(t, h, ws, user, "OWNER", "POST", "/restore", `{"revision":1,"base_rev":1,"mutation_id":"unsupported-restore"}`), 503)
	req := pagesGrantRequest(t, "POST", "/api/v1/pages/report/artifact/copies", ws, user, "OWNER", `{"source_revision":1,"slug":"unsupported-copy","name":"Copy","mutation_id":"unsupported-copy"}`)
	req.SetPathValue("slug", "report")
	rr := httptest.NewRecorder()
	h.CopyArtifact(rr, req)
	artifactExpect(t, rr, 503)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM page_artifact_revisions`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unsupported source created revision: %d %v", n, err)
	}
}
