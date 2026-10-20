package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

// These regressions exercise the public handler with real credential, membership,
// chat and Page persistence. No engine is needed to inspect confirmed metadata.
func previewDocumentRequest(t *testing.T, h *PageHandler, workspace, user, chat, page string) *http.Request {
	t.Helper()
	r := documentResultsRequest(t, h, workspace, user)
	r.URL, _ = url.Parse("/api/v1/chats/" + url.PathEscape(chat) + "/artifact-context/preview?page_id=" + url.QueryEscape(page))
	r.SetPathValue("chatId", chat)
	return r
}

func callDocumentPreview(h *PageHandler, r *http.Request) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	h.PreviewChatDocumentContext(response, r)
	return response
}

func documentPreviewAuthoritySnapshot(t *testing.T, h *PageHandler) []int64 {
	t.Helper()
	var snapshot []int64
	for _, query := range []string{
		`SELECT count(*) FROM document_iteration_intents`,
		`SELECT count(*) FROM artifact_run_principals`,
		`SELECT count(*) FROM document_creation_consents`,
		`SELECT count(*) FROM page_grants`,
		`SELECT count(*) FROM document_receipts`,
		`SELECT count(*) FROM document_checkpoints`,
		`SELECT count(*) FROM journal_entries`,
		`SELECT COALESCE(sum(head_rev),0) FROM documents`,
		`SELECT COALESCE(sum(generation),0) FROM documents`,
	} {
		var value int64
		if err := h.db.QueryRow(query).Scan(&value); err != nil {
			t.Fatal(query, err)
		}
		snapshot = append(snapshot, value)
	}
	return snapshot
}

func TestChatArtifactPreviewExactMetadataWithoutAuthority(t *testing.T) {
	for _, protected := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary-private-chat", true: "protected-output-chat"}[protected], func(t *testing.T) {
			h, workspace, owner, saved := documentResultsFixture(t)
			h.db.SetMaxOpenConns(1)
			chat, audience := "preview-chat", "author_and_workspace_operators"
			if protected {
				chat, audience = "principal-chat", "author_and_workspace_owners"
			} else {
				mustExec(t, h.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES(?,?,'principal-agent',?,'private')`, chat, workspace, owner)
			}
			mustExec(t, h.db, `UPDATE documents SET head_rev=17,generation=4 WHERE page_id=?`, saved.PageID)
			h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
				t.Fatal("metadata preview called the content engine")
				return nil, 500, nil
			}
			r := previewDocumentRequest(t, h, workspace, owner, chat, saved.PageID)
			before := documentPreviewAuthoritySnapshot(t, h)
			// Fail even a same-row UPDATE, grant binding, or writer-fence touch.
			for _, table := range []string{"documents", "document_iteration_intents", "artifact_run_principals", "document_creation_consents", "page_grants", "document_receipts", "document_checkpoints", "document_writer_fence"} {
				for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
					mustExec(t, h.db, fmt.Sprintf("CREATE TRIGGER preview_readonly_%s_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'preview must not write authority'); END", table, operation, operation, table))
				}
			}
			response := callDocumentPreview(h, r)
			creationExpect(t, response, http.StatusOK)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("preview must not be cached", response.Header())
			}
			var actual any
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"chat":       map[string]any{"id": chat, "visibility": "private", "audience": audience},
				"target":     map[string]any{"page_id": saved.PageID, "type": "document", "audience": "author_and_workspace_owners", "expected": map[string]any{"schema": float64(3), "rev": float64(17), "generation": float64(4)}},
				"can_update": false, "reason": "targeted_iteration_unavailable",
			}
			if !reflect.DeepEqual(actual, want) {
				t.Fatalf("closed preview DTO: got %#v want %#v", actual, want)
			}
			if after := documentPreviewAuthoritySnapshot(t, h); !reflect.DeepEqual(before, after) {
				t.Fatalf("read-only preview created authority or changed content: before=%v after=%v", before, after)
			}
		})
	}
}

func TestChatArtifactPreviewUnavailableTargetsAreIndistinguishable(t *testing.T) {
	for _, change := range []string{"missing-page", "missing-chat", "foreign-Page-owner", "foreign-chat-author", "admin-inspector", "owner-inspector", "group-chat", "shared-Page", "folder-Page", "missing-document", "schema2", "schema4", "cross-workspace", "restricted", "membership-removed", "suspended"} {
		t.Run(change, func(t *testing.T) {
			h, workspace, owner, saved := documentResultsFixture(t)
			chat, page, caller := "principal-chat", saved.PageID, owner
			switch change {
			case "missing-page":
				page = "missing-page"
			case "missing-chat":
				chat = "missing-chat"
			case "foreign-Page-owner", "foreign-chat-author", "admin-inspector", "owner-inspector":
				role := "MEMBER"
				if change == "admin-inspector" {
					role = "ADMIN"
				}
				if change == "owner-inspector" {
					role = "OWNER"
				}
				pagesSeedUser(t, h, workspace, "preview-other", "preview-other@example.invalid", role)
				if change == "foreign-Page-owner" {
					mustExec(t, h.db, `UPDATE pages SET owner_user_id='preview-other' WHERE id=?`, page)
				} else if change == "foreign-chat-author" {
					mustExec(t, h.db, `UPDATE chats SET created_by='preview-other' WHERE id=?`, chat)
				} else {
					caller = "preview-other"
				}
			case "group-chat":
				mustExec(t, h.db, `UPDATE chats SET visibility='group' WHERE id=?`, chat)
			case "shared-Page":
				mustExec(t, h.db, `UPDATE pages SET private_audience=0 WHERE id=?`, page)
			case "folder-Page":
				mustExec(t, h.db, `INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('preview-folder',?,'preview-folder','Private folder','principal-crew','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, workspace)
				mustExec(t, h.db, `UPDATE pages SET folder_id='preview-folder' WHERE id=?`, page)
			case "missing-document":
				mustExec(t, h.db, `DELETE FROM documents WHERE page_id=?`, page)
			case "schema2":
				mustExec(t, h.db, `UPDATE documents SET schema_version=2 WHERE page_id=?`, page)
			case "schema4":
				mustExec(t, h.db, `UPDATE documents SET schema_version=4 WHERE page_id=?`, page)
			case "cross-workspace":
				mustExec(t, h.db, `INSERT INTO workspaces(id,name,slug) VALUES('preview-other-workspace','Other workspace','preview-other')`)
				mustExec(t, h.db, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('preview-other-member','preview-other-workspace',?,'OWNER')`, owner)
				workspace = "preview-other-workspace"
			case "restricted":
				mustExec(t, h.db, `UPDATE workspace_members SET access_mode='restricted' WHERE user_id=?`, owner)
			case "membership-removed":
				mustExec(t, h.db, `DELETE FROM workspace_members WHERE user_id=?`, owner)
			case "suspended":
				mustExec(t, h.db, `UPDATE users SET suspended_at='2026-01-01T00:00:00Z' WHERE id=?`, owner)
			}
			response := callDocumentPreview(h, previewDocumentRequest(t, h, workspace, caller, chat, page))
			creationExpect(t, response, 404)
			var actual map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || !reflect.DeepEqual(actual, map[string]any{"error": "Document context not found"}) {
				t.Fatalf("all unavailable objects need the same metadata-free denial: %s (%v)", response.Body.String(), err)
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("denial cached")
			}
		})
	}
}

func TestChatArtifactPreviewCurrentCredentialAndConfiguredService(t *testing.T) {
	for _, change := range []string{"session-revoked", "cli-revoked", "cli-narrowed", "agent", "internal-workspace", "run-principal", "unknown-credential", "disabled", "disabled-foreign-chat"} {
		t.Run(change, func(t *testing.T) {
			h, workspace, owner, saved := documentResultsFixture(t)
			r := previewDocumentRequest(t, h, workspace, owner, "principal-chat", saved.PageID)
			want := 401
			switch change {
			case "session-revoked":
				mustExec(t, h.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z'`)
			case "cli-revoked", "cli-narrowed":
				const token = "crewship_cli_document_preview_test"
				mustExec(t, h.db, `INSERT INTO cli_tokens(id,user_id,name,token_hash,tier,scopes) VALUES('preview-cli',?,'preview',?,'STANDARD','["pages:read"]')`, owner, hashStandard(token))
				r.Header.Set("Authorization", "Bearer "+token)
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, AuthKindCLIToken))
				if change == "cli-revoked" {
					mustExec(t, h.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE id='preview-cli'`)
				} else {
					mustExec(t, h.db, `UPDATE cli_tokens SET scopes='["chat"]' WHERE id='preview-cli'`)
					want = 403
				}
			case "agent":
				r.Header.Set("X-Internal-Token", "synthetic-crew-ipc")
			case "internal-workspace":
				r = r.WithContext(context.WithValue(r.Context(), ctxInternalTokenWS, workspace))
			case "run-principal":
				r = r.WithContext(context.WithValue(r.Context(), artifactRunKey{}, &artifactRunAuthority{AgentID: "principal-agent"}))
			case "unknown-credential":
				r = r.WithContext(context.WithValue(r.Context(), ctxAuthKind, "unknown"))
			case "disabled", "disabled-foreign-chat":
				h.documentCall = nil
				want = 503
				if change == "disabled-foreign-chat" {
					r.SetPathValue("chatId", "absent-chat")
					want = 404
				}
			}
			response := callDocumentPreview(h, r)
			creationExpect(t, response, want)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("unavailable response cached")
			}
			for _, private := range []string{saved.PageID, saved.Title, saved.Slug, "schema", "generation"} {
				if strings.Contains(response.Body.String(), private) {
					t.Fatalf("denial exposed %q: %s", private, response.Body.String())
				}
			}
		})
	}
}

// An infrastructure/read failure is not evidence that access was withdrawn.
// Keep the private DTO closed, while allowing callers to distinguish retryable
// server failure from the same 404 used for an unreadable or missing target.
func TestChatArtifactPreviewPersistenceFailureIsNotDenial(t *testing.T) {
	h, workspace, owner, saved := documentResultsFixture(t)
	h.documentCall = func(context.Context, string, string, string, string, any) ([]byte, int, error) {
		t.Fatal("metadata preview attempted an engine RPC during persistence failure")
		return nil, 500, nil
	}
	r := previewDocumentRequest(t, h, workspace, owner, "principal-chat", saved.PageID)
	mustExec(t, h.db, `ALTER TABLE page_panels RENAME TO preview_unavailable_panels`)
	response := callDocumentPreview(h, r)
	creationExpect(t, response, http.StatusInternalServerError)
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("failed metadata read must not be cached")
	}
	var actual map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &actual); err != nil || len(actual) != 1 || actual["error"] == nil {
		t.Fatalf("server failure must remain a closed error envelope: %s (%v)", response.Body.String(), err)
	}
	for _, private := range []string{saved.PageID, saved.Title, saved.Slug, "principal-chat", "preview_unavailable_panels", `"target"`, `"expected"`} {
		if strings.Contains(response.Body.String(), private) {
			t.Fatalf("server failure disclosed %q: %s", private, response.Body.String())
		}
	}
}
