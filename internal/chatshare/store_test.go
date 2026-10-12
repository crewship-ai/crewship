package chatshare

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func shareTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d := testutil.MigratedDB(t)
	for _, statement := range []string{
		`INSERT INTO users(id,email) VALUES ('owner','owner@t.test'),('peer','peer@t.test'),('admin','admin@t.test')`,
		`INSERT INTO workspaces(id,name,slug) VALUES ('ws','Test','test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES ('m1','ws','owner','MEMBER'),('m2','ws','peer','MEMBER'),('m3','ws','admin','ADMIN')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES ('crew','ws','Crew','crew')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES ('agent','ws','crew','Agent','agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,mode,origin) VALUES ('chat','ws','agent','owner','CHAT','UI')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,mode,origin) VALUES ('work','ws','agent','owner','MISSION','AGENT')`,
	} {
		if _, err := d.Exec(statement); err != nil {
			t.Fatalf("seed %s: %v", statement, err)
		}
	}
	return d.DB
}

func TestShareLifecycleAndCurrentAuthority(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	if _, _, err := s.Create(ctx, "ws", "agent", "chat", "peer", time.Hour); !errors.Is(err, ErrDenied) {
		t.Fatalf("non-creator created share: %v", err)
	}
	if _, _, err := s.Create(ctx, "ws", "agent", "work", "admin", time.Hour); !errors.Is(err, ErrDenied) {
		t.Fatalf("machine chat shared: %v", err)
	}
	if _, _, err := s.Create(ctx, "ws", "agent", "chat", "owner", MaxTTL+time.Second); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlong share: %v", err)
	}
	if _, _, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Millisecond); !errors.Is(err, ErrInvalid) {
		t.Fatalf("subsecond share: %v", err)
	}
	g, token, err := s.Create(ctx, "ws", "agent", "chat", "owner", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, TokenPrefix) || g.ExpiresAt.Sub(g.CreatedAt) != DefaultTTL {
		t.Fatalf("wrong token or default expiry: %+v %q", g, token)
	}
	var stored []byte
	if err := db.QueryRow(`SELECT token_hash FROM chat_read_shares WHERE id=?`, g.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), token) || len(stored) != 32 {
		t.Fatal("plaintext token stored")
	}
	if _, err := s.Validate(ctx, g.ID, "cshr_"+strings.Repeat("A", 43)); !errors.Is(err, ErrDenied) {
		t.Fatalf("wrong token accepted: %v", err)
	}
	if got, err := s.Validate(ctx, g.ID, token); err != nil || got.ChatID != "chat" {
		t.Fatalf("valid token: %+v %v", got, err)
	}
	if list, err := s.List(ctx, "ws", "agent", "chat", "owner"); err != nil || len(list) != 1 {
		t.Fatalf("owner list: %+v %v", list, err)
	}
	if _, err := s.List(ctx, "ws", "agent", "chat", "peer"); !errors.Is(err, ErrDenied) {
		t.Fatalf("peer listed share: %v", err)
	}
	if err := s.Revoke(ctx, "ws", "agent", "chat", g.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("revoked share accepted: %v", err)
	}

	g2, token2, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM workspace_members WHERE workspace_id='ws' AND user_id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g2.ID, token2); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed issuer still authorized: %v", err)
	}
	if _, err := s.List(ctx, "ws", "agent", "chat", "owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed issuer still listed shares: %v", err)
	}
}

func TestShareExpirationAndAgentDeletion(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	g, token, err := s.Create(ctx, "ws", "agent", "chat", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE chat_read_shares SET created_at=?, expires_at=? WHERE id=?`,
		formatTime(time.Now().Add(-time.Hour)), formatTime(time.Now().Add(-time.Second)), g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("expired share accepted: %v", err)
	}
	g2, token2, err := s.Create(ctx, "ws", "agent", "chat", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE agents SET deleted_at=? WHERE id='agent'`, formatTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g2.ID, token2); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleted agent share accepted: %v", err)
	}
}

func TestShareCreatorRevokeAndAdminDemotion(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	g, token, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, "ws", "agent", "chat", g.ID, "owner"); err != nil {
		t.Fatalf("creator revoke: %v", err)
	}
	if _, err := s.Validate(ctx, g.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("creator-revoked share accepted: %v", err)
	}
	g2, token2, err := s.Create(ctx, "ws", "agent", "chat", "admin", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE workspace_id='ws' AND user_id='admin'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g2.ID, token2); !errors.Is(err, ErrDenied) {
		t.Fatalf("demoted issuer share accepted: %v", err)
	}
}

func TestShareParentDeletionAndReassociation(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	g, token, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		invalidate string
		restore    string
	}{
		{"soft-deleted crew", `UPDATE crews SET deleted_at='2026-09-27T00:00:00Z' WHERE id='crew'`, `UPDATE crews SET deleted_at=NULL WHERE id='crew'`},
		{"soft-deleted workspace", `UPDATE workspaces SET deleted_at='2026-09-27T00:00:00Z' WHERE id='ws'`, `UPDATE workspaces SET deleted_at=NULL WHERE id='ws'`},
		{"chat moved to other agent", `UPDATE chats SET agent_id='agent2' WHERE id='chat'`, `UPDATE chats SET agent_id='agent' WHERE id='chat'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "chat moved to other agent" {
				if _, err := db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES ('agent2','ws','crew','Other','other')`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec(tc.invalidate); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Validate(ctx, g.ID, token); !errors.Is(err, ErrDenied) {
				t.Fatalf("stale parent share accepted: %v", err)
			}
			if _, _, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Hour); !errors.Is(err, ErrDenied) {
				t.Fatalf("created with stale parent: %v", err)
			}
			if _, err := db.Exec(tc.restore); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Validate(ctx, g.ID, token); err != nil {
				t.Fatalf("restored parent rejected: %v", err)
			}
		})
	}
	if _, err := db.Exec(`DELETE FROM users WHERE id='owner'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleted issuer share accepted: %v", err)
	}
}

func TestShareSurvivesDatabaseReopenAndBindsChat(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	ctx := context.Background()
	g, token, err := s.Create(ctx, "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO chats(id,workspace_id,agent_id,created_by,mode,origin) VALUES ('other-chat','ws','agent','owner','CHAT','UI')`); err != nil {
		t.Fatal(err)
	}
	other, otherToken, err := s.Create(ctx, "ws", "agent", "other-chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Validate(ctx, g.ID, otherToken); !errors.Is(err, ErrDenied) {
		t.Fatalf("other-chat token accepted: %v", err)
	}
	if _, err := s.Validate(ctx, other.ID, token); !errors.Is(err, ErrDenied) {
		t.Fatalf("first-chat token accepted for other chat: %v", err)
	}
	var seq int
	var name, dbPath string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &dbPath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := database.Open("file:" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got, err := NewStore(reopened.DB).Validate(ctx, g.ID, token); err != nil || got.ChatID != "chat" {
		t.Fatalf("durable share after reopen: %+v %v", got, err)
	}
}
