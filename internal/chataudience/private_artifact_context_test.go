package chataudience

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func artifactConsent(t *testing.T, db *sql.DB, id, human string) {
	t.Helper()
	audienceExec(t, db, `INSERT INTO conversation_messages(id,session_id,agent_id,role,content,author_user_id,ts) VALUES(?,'private','agent','user','Create',?,'2026-10-19T00:00:00Z')`, id, human)
	audienceExec(t, db, `INSERT INTO document_creation_consents(message_id,workspace_id,human_user_id,chat_id,submission_id,created_at) VALUES(?,'ws',?,'private',?,'2026-10-19T00:00:00Z')`, id, human, id)
}
func artifactCreation(t *testing.T, db *sql.DB) {
	t.Helper()
	audienceExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('seal-page','ws','seal-page','Fixture','author','{}')`)
	audienceExec(t, db, `INSERT INTO documents(workspace_id,page_id,schema_version,snapshot,projection) VALUES('ws','seal-page',3,x'00','{}')`)
	audienceExec(t, db, `INSERT INTO document_creations(page_id,workspace_id,run_id,agent_id,human_user_id,source_chat_id,submission_id,mutation_id,request_digest,created_at) VALUES('seal-page','ws','creation-run','agent','author','private','original','initial',?,'2026-10-19T00:00:00Z')`, strings.Repeat("a", 64))
}
func TestPrivateArtifactContextDurableClassification(t *testing.T) {
	for _, lifecycle := range []string{"initial", "expired", "revoked", "consumed", "deleted result", "published result"} {
		t.Run(lifecycle, func(t *testing.T) {
			db := audienceDB(t)
			artifactConsent(t, db, "consent", "author")
			switch lifecycle {
			case "expired":
				audienceExec(t, db, `UPDATE document_creation_consents SET run_id='old-run',expires_at='2000-01-01T00:00:00Z'`)
			case "revoked":
				audienceExec(t, db, `UPDATE document_creation_consents SET revoked_at='2026-10-19T00:00:00Z'`)
			case "consumed":
				audienceExec(t, db, `UPDATE document_creation_consents SET consumed_at='2026-10-19T00:00:00Z'`)
			case "deleted result", "published result":
				artifactCreation(t, db)
				if lifecycle == "deleted result" {
					audienceExec(t, db, `DELETE FROM pages WHERE id='seal-page'`)
				} else {
					audienceExec(t, db, `UPDATE pages SET private_audience=0 WHERE id='seal-page'`)
				}
			}
			got, err := ResolvePrivateArtifactContext(t.Context(), db, "ws", "private")
			if err != nil || !got.Sealed || got.AuthorUserID != "author" || got.ChatID != "private" || got.WorkspaceID != "ws" {
				t.Fatalf("durable seal: %#v %v", got, err)
			}
			for _, tc := range []struct {
				user, role string
				want       PrivateArtifactDecision
			}{{"author", "MEMBER", ArtifactContextAuthor}, {"admin", "ADMIN", ArtifactContextDenied}, {"owner", "OWNER", ArtifactContextOwner}} {
				if got.Decide(tc.user, tc.role) != tc.want {
					t.Errorf("decision %s: %v", tc.user, got.Decide(tc.user, tc.role))
				}
			}
			metadata := got.AuditMetadata()
			if len(metadata) != 2 || metadata["owner_user_id"] != "author" || metadata["output_source"] != "conversation" {
				t.Fatalf("unsafe audit metadata: %#v", metadata)
			}
		})
	}
	db := audienceDB(t)
	got, err := ResolvePrivateArtifactContext(t.Context(), db, "ws", "private")
	if err != nil || got.Sealed || got.Decide("admin", "ADMIN") != ArtifactContextUnsealed {
		t.Fatalf("ordinary context changed: %#v %v", got, err)
	}
	artifactCreation(t, db)
	got, err = ResolvePrivateArtifactContext(t.Context(), db, "ws", "private")
	if err != nil || !got.Sealed || got.AuthorUserID != "author" {
		t.Fatalf("creation fallback: %#v %v", got, err)
	}
	if _, err = ResolvePrivateArtifactContext(t.Context(), db, "foreign", "private"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("workspace mismatch: %v", err)
	}
}
func TestPrivateArtifactContextMalformedProvenanceFailsClosed(t *testing.T) {
	for _, change := range []string{"foreign author", "mixed authors", "wrong workspace", "wrong message agent", "wrong message author", "group", "creation wrong agent"} {
		t.Run(change, func(t *testing.T) {
			db := audienceDB(t)
			artifactConsent(t, db, "consent", "author")
			switch change {
			case "foreign author":
				audienceExec(t, db, `UPDATE chats SET created_by='other' WHERE id='private'`)
			case "mixed authors":
				artifactConsent(t, db, "other-consent", "other")
			case "wrong workspace":
				audienceExec(t, db, `UPDATE chats SET workspace_id='foreign' WHERE id='private'`)
			case "wrong message agent":
				audienceExec(t, db, `UPDATE conversation_messages SET agent_id='other-agent' WHERE id='consent'`)
			case "wrong message author":
				audienceExec(t, db, `UPDATE conversation_messages SET author_user_id='other' WHERE id='consent'`)
			case "group":
				audienceExec(t, db, `UPDATE chats SET visibility='group' WHERE id='private'`)
			case "creation wrong agent":
				artifactCreation(t, db)
				audienceExec(t, db, `UPDATE document_creations SET agent_id='other-agent'`)
			}
			var ws string
			if err := db.QueryRow(`SELECT workspace_id FROM chats WHERE id='private'`).Scan(&ws); err != nil {
				t.Fatal(err)
			}
			if _, err := ResolvePrivateArtifactContext(t.Context(), db, ws, "private"); err == nil {
				t.Fatal("malformed source treated as ordinary or valid")
			}
			var readable, shareable bool
			if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM chats c JOIN workspace_members wm ON wm.workspace_id=c.workspace_id WHERE c.id='private' AND wm.user_id='owner' AND (`+PrivateArtifactContextSQL+`)),EXISTS(SELECT 1 FROM chats c WHERE c.id='private' AND (`+NoPrivateArtifactContextSQL+`))`).Scan(&readable, &shareable); err != nil {
				t.Fatal(err)
			}
			if readable || shareable {
				t.Fatalf("malformed source admitted read=%v share=%v", readable, shareable)
			}
		})
	}
	db := audienceDB(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolvePrivateArtifactContext(t.Context(), db, "ws", "private"); err == nil {
		t.Fatal("database failure admitted classification")
	}
}
func TestPrivateArtifactConversationReadAuditAndLiveAuthority(t *testing.T) {
	db := audienceDB(t)
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	// The ordinary administrative policy is unchanged in the absence of provenance.
	if ok, err := CanReadArtifactConversation(ctx, db, "private", "admin", "ws", true); err != nil || !ok {
		t.Fatalf("ordinary admin: %v %v", ok, err)
	}
	artifactConsent(t, db, "consent", "author")
	for _, tc := range []struct {
		user    string
		allowed bool
	}{{"author", true}, {"admin", false}, {"owner", true}, {"outsider", false}} {
		ok, err := CanReadArtifactConversation(ctx, db, "private", tc.user, "ws", true)
		if err != nil || ok != tc.allowed {
			t.Errorf("sealed %s: %v %v", tc.user, ok, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM audit_logs WHERE action='artifact.private_owner_accessed' AND entity_type='chat' AND entity_id='private' AND user_id='owner' AND json_extract(metadata,'$.owner_user_id')='author' AND json_extract(metadata,'$.output_source')='conversation'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("required content-free context audit count=%d err=%v", count, err)
	}
	audienceExec(t, db, `CREATE TRIGGER refuse_conversation_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'fixture audit unavailable'); END`)
	if ok, err := CanReadArtifactConversation(ctx, db, "private", "owner", "ws", true); ok || err == nil {
		t.Fatalf("audit failure: %v %v", ok, err)
	}
	if ok, err := CanReadArtifactConversation(ctx, db, "private", "author", "ws", true); err != nil || !ok {
		t.Fatalf("author unnecessarily needs foreign audit: %v %v", ok, err)
	}
	audienceExec(t, db, `UPDATE users SET suspended_at='2026-10-19T00:00:00Z' WHERE id='author'`)
	if ok, err := CanReadArtifactConversation(ctx, db, "private", "author", "ws", true); err != nil || ok {
		t.Fatalf("suspended author: %v %v", ok, err)
	}
}

func TestPrivateArtifactConversationRestrictedReadBoundary(t *testing.T) {
	db := audienceDB(t)
	for _, trustedOnly := range []bool{false, true} {
		ok, err := CanReadArtifactConversation(t.Context(), db, "restricted-own", "restricted", "ws", trustedOnly)
		if err != nil || ok == trustedOnly {
			t.Fatalf("restricted original policy trustedOnly=%v: %v %v", trustedOnly, ok, err)
		}
	}
}

// A historical actor annotation is not authority. Losing it after the original
// audience was durably classified cannot either unseal the chat or erase custody.
func TestPrivateArtifactContextHistoricalActorLossRetainsKnownAudience(t *testing.T) {
	db := audienceDB(t)
	artifactConsent(t, db, "original-consent", "author")
	artifactCreation(t, db)
	audienceExec(t, db, `DELETE FROM document_creation_consents`)
	audienceExec(t, db, `UPDATE document_creations SET human_user_id=NULL`)
	for _, tc := range []struct {
		user    string
		allowed bool
	}{{"author", true}, {"owner", true}, {"admin", false}} {
		ok, err := CanReadArtifactConversation(t.Context(), db, "private", tc.user, "ws", true)
		if err != nil || ok != tc.allowed {
			t.Errorf("retained audience %s: %v %v", tc.user, ok, err)
		}
	}
}

func TestPrivateArtifactCatalogBatchFiltersAndAudits(t *testing.T) {
	db := audienceDB(t)
	db.SetMaxOpenConns(1)
	artifactConsent(t, db, "consent", "author")
	for _, tc := range []struct {
		user    string
		private bool
	}{{"author", true}, {"owner", true}, {"admin", false}} {
		allowed, err := FilterReadableArtifactConversations(t.Context(), db, "ws", tc.user, []string{"private", "group", "missing"})
		if err != nil || allowed["private"] != tc.private || !allowed["group"] || allowed["missing"] {
			t.Errorf("batch %s: %v %v", tc.user, allowed, err)
		}
	}
	audienceExec(t, db, `CREATE TRIGGER reject_batch_audit BEFORE INSERT ON audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	if _, err := FilterReadableArtifactConversations(t.Context(), db, "ws", "owner", []string{"private"}); err == nil {
		t.Fatal("catalog released protected output despite audit failure")
	}
	if _, err := FilterReadableArtifactConversations(t.Context(), db, "ws", "author", make([]string, 101)); err == nil {
		t.Fatal("unbounded catalog accepted")
	}
}
