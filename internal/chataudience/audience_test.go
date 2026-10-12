package chataudience

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/testutil"
)

func audienceDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	audienceExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('ws','Workspace','ws'),('foreign','Foreign','foreign')`)
	audienceExec(t, db, `INSERT INTO agents(id,workspace_id,name,slug) VALUES('agent','ws','Agent','agent'),('other-agent','ws','Other','other'),('foreign-agent','foreign','Foreign','foreign')`)
	for _, member := range []struct{ user, role, mode, workspace string }{
		{"author", "MEMBER", "trusted", "ws"},
		{"other", "MEMBER", "trusted", "ws"},
		{"owner", "OWNER", "trusted", "ws"},
		{"admin", "ADMIN", "trusted", "ws"},
		{"restricted", "MEMBER", "restricted", "ws"},
		{"restricted-admin", "ADMIN", "restricted", "ws"},
		{"outsider", "OWNER", "trusted", "foreign"},
		{"hybrid", "MEMBER", "trusted", "ws"},
	} {
		audienceExec(t, db, `INSERT INTO users(id,email) VALUES(?,?)`, member.user, member.user+"@example.test")
		audienceExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES(?,?,?,?,?)`, member.user+"-member", member.workspace, member.user, member.role, member.mode)
	}
	audienceExec(t, db, `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('hybrid-foreign','foreign','hybrid','MEMBER','restricted')`)
	for _, chat := range []struct{ id, creator, visibility, mode, origin string }{
		{"private", "author", "private", "CHAT", "UI"},
		{"group", "author", "group", "CHAT", "UI"},
		{"restricted-own", "restricted", "private", "CHAT", "UI"},
		{"outsider-own", "outsider", "private", "CHAT", "UI"},
		{"hybrid-own", "hybrid", "private", "CHAT", "UI"},
		{"legacy", "", "private", "CHAT", ""},
		{"unknown-visibility", "author", "unexpected", "CHAT", "UI"},
		{"mission", "", "private", "MISSION", ""},
		{"routine", "", "private", "CHAT", "ROUTINE"},
		{"cron", "", "private", "CHAT", "CRON"},
		{"webhook", "", "private", "CHAT", "WEBHOOK"},
		{"agent-work", "", "private", "CHAT", "AGENT"},
	} {
		var creator any
		if chat.creator != "" {
			creator = chat.creator
		}
		audienceExec(t, db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,mode,origin) VALUES(?,'ws','agent',?,?,?,?)`, chat.id, creator, chat.visibility, chat.mode, chat.origin)
	}
	for _, participant := range []string{"other", "restricted", "restricted-admin", "outsider"} {
		audienceExec(t, db, `INSERT INTO chat_participants(chat_id,user_id,role) VALUES('group',?,'member')`, participant)
	}
	audienceExec(t, db, `INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('chat-grant','restricted-member','agent','agent','chat','owner','2026-10-02T00:00:00Z')`)
	return db
}

func audienceExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatalf("fixture mutation: %v", err)
	}
}

func TestAudienceAuthorizationMatrix(t *testing.T) {
	db := audienceDB(t)
	for _, tc := range []struct {
		name, chat, user string
		read, trusted    bool
	}{
		{"private creator", "private", "author", true, true},
		{"private peer", "private", "other", false, false},
		{"private operator", "private", "owner", true, true},
		{"private administrator", "private", "admin", true, true},
		{"group creator", "group", "author", true, true},
		{"group participant", "group", "other", true, true},
		{"foreign creator", "outsider-own", "outsider", false, false},
		{"foreign participant", "group", "outsider", false, false},
		{"foreign operator", "private", "outsider", false, false},
		{"restricted owner with exact chat grant", "restricted-own", "restricted", true, false},
		{"restricted participant with exact chat grant", "group", "restricted", true, false},
		{"restricted grant cannot read another private chat", "private", "restricted", false, false},
		{"restricted administrator inherits no role authority", "private", "restricted-admin", false, false},
		{"restricted participant needs a grant", "group", "restricted-admin", false, false},
		{"restricted membership elsewhere blocks shared runtime", "hybrid-own", "hybrid", true, false},
		{"legacy provenance fails closed", "legacy", "owner", false, false},
		{"unknown visibility fails closed for creator", "unknown-visibility", "author", false, false},
		{"unknown visibility fails closed for operator", "unknown-visibility", "admin", false, false},
		{"missing chat", "absent", "owner", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CanRead(t.Context(), db, tc.chat, tc.user)
			if err != nil || got != tc.read {
				t.Fatalf("CanRead=%v err=%v, want %v", got, err, tc.read)
			}
			got, err = CanReadInWorkspace(t.Context(), db, tc.chat, tc.user, "ws")
			if err != nil || got != tc.read {
				t.Fatalf("selected workspace read=%v err=%v, want %v", got, err, tc.read)
			}
			got, err = CanReadInWorkspace(t.Context(), db, tc.chat, tc.user, "foreign")
			if err != nil || got {
				t.Fatalf("foreign selected workspace read=%v err=%v", got, err)
			}
			got, err = CanReadTrusted(t.Context(), db, tc.chat, tc.user)
			if err != nil || got != tc.trusted {
				t.Fatalf("shared runtime read=%v err=%v, want %v", got, err, tc.trusted)
			}
		})
	}
	for _, chat := range []string{"mission", "routine", "cron", "webhook", "agent-work"} {
		for _, user := range []string{"owner", "admin", "author", "restricted-admin"} {
			got, err := CanRead(t.Context(), db, chat, user)
			want := user == "owner" || user == "admin"
			if err != nil || got != want {
				t.Errorf("machine chat %s user %s: read=%v err=%v, want %v", chat, user, got, err, want)
			}
		}
	}
}

func TestAudienceRevocationAndExactGrant(t *testing.T) {
	for _, tc := range []struct{ name, chat, user, mutation string }{
		{"workspace member removed", "private", "author", `DELETE FROM workspace_members WHERE id='author-member'`},
		{"group participant removed", "group", "other", `DELETE FROM chat_participants WHERE chat_id='group' AND user_id='other'`},
		{"restricted grant revoked", "group", "restricted", `DELETE FROM access_grants WHERE id='chat-grant'`},
		{"run grant is not chat authority", "restricted-own", "restricted", `UPDATE access_grants SET operation='run' WHERE id='chat-grant'`},
		{"another agent grant is not chat authority", "restricted-own", "restricted", `UPDATE access_grants SET agent_id='other-agent' WHERE id='chat-grant'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := audienceDB(t)
			if allowed, err := CanRead(t.Context(), db, tc.chat, tc.user); err != nil || !allowed {
				t.Fatalf("initial authority absent: %v %v", allowed, err)
			}
			audienceExec(t, db, tc.mutation)
			if allowed, err := CanRead(t.Context(), db, tc.chat, tc.user); err != nil || allowed {
				t.Fatalf("revoked authority remains: %v %v", allowed, err)
			}
		})
	}
}

func TestRestrictedAdministratorChatGrantDoesNotWidenAudience(t *testing.T) {
	db := audienceDB(t)
	audienceExec(t, db, `INSERT INTO access_grants(id,member_id,resource_kind,agent_id,operation,created_by,created_at) VALUES('admin-chat-grant','restricted-admin-member','agent','agent','chat','owner','2026-10-02T00:00:00Z')`)
	for _, tc := range []struct {
		chat string
		want bool
	}{{"group", true}, {"private", false}, {"routine", false}} {
		if allowed, err := CanRead(t.Context(), db, tc.chat, "restricted-admin"); err != nil || allowed != tc.want {
			t.Errorf("restricted administrator chat %s: allowed=%v err=%v, want %v", tc.chat, allowed, err, tc.want)
		}
	}
	if allowed, err := CanReadTrusted(t.Context(), db, "group", "restricted-admin"); err != nil || allowed {
		t.Fatalf("scoped grant admitted shared-runtime context: %v %v", allowed, err)
	}
}

func TestAudienceInvalidInputsAndUnavailableStorage(t *testing.T) {
	db := audienceDB(t)
	for _, tc := range []struct {
		db         *sql.DB
		chat, user string
	}{{nil, "private", "author"}, {db, "", "author"}, {db, "private", ""}} {
		if allowed, err := CanRead(t.Context(), tc.db, tc.chat, tc.user); allowed || err != nil {
			t.Fatalf("invalid audience input: %v %v", allowed, err)
		}
		if allowed, err := CanReadTrusted(t.Context(), tc.db, tc.chat, tc.user); allowed || err != nil {
			t.Fatalf("invalid trusted input: %v %v", allowed, err)
		}
		if receipt, err := CaptureTrusted(t.Context(), tc.db, tc.chat, tc.user, nil); receipt != nil || err != nil {
			t.Fatalf("invalid receipt input: %+v %v", receipt, err)
		}
	}
	if allowed, err := CanReadInWorkspace(t.Context(), db, "private", "author", ""); allowed || err != nil {
		t.Fatalf("unselected workspace admitted: %v %v", allowed, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, check := range []func(context.Context, *sql.DB, string, string) (bool, error){CanRead, CanReadTrusted} {
		if allowed, err := check(ctx, db, "private", "author"); allowed || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled authorization: %v %v", allowed, err)
		}
	}
	if receipt, err := CaptureTrusted(ctx, db, "private", "author", nil); receipt != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission: %+v %v", receipt, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if allowed, err := CanRead(t.Context(), db, "private", "author"); allowed || err == nil {
		t.Fatalf("storage outage did not fail closed: %v %v", allowed, err)
	}
}
