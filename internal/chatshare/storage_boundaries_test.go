package chatshare

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestShareOperationsRejectIncompleteScope(t *testing.T) {
	s := NewStore(nil)
	for _, args := range [][4]string{{"", "agent", "chat", "owner"}, {"ws", "", "chat", "owner"}, {"ws", "agent", "", "owner"}, {"ws", "agent", "chat", ""}} {
		if _, token, err := s.Create(t.Context(), args[0], args[1], args[2], args[3], time.Hour); !errors.Is(err, ErrInvalid) || token != "" {
			t.Fatalf("invalid create = %v", err)
		}
		if list, err := s.List(t.Context(), args[0], args[1], args[2], args[3]); !errors.Is(err, ErrInvalid) || list != nil {
			t.Fatalf("invalid list = %v", err)
		}
		if err := s.Revoke(t.Context(), args[0], args[1], args[2], "share", args[3]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid revoke = %v", err)
		}
	}
	if err := s.Revoke(t.Context(), "ws", "agent", "chat", "", "owner"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty grant ID = %v", err)
	}
	if _, err := s.Validate(t.Context(), "share", "malformed"); !errors.Is(err, ErrDenied) {
		t.Fatalf("invalid token = %v", err)
	}
}

func TestShareStorageOutageIsNotReportedAsAnAbsentGrant(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	g, token, err := s.Create(t.Context(), "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(t.Context(), "ws", "agent", "chat", "missing", "owner"); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing grant revoke = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for name, operation := range map[string]func() error{
		"create": func() error {
			_, token, err := s.Create(t.Context(), "ws", "agent", "chat", "owner", time.Hour)
			if token != "" {
				t.Error("failed write exposed a token")
			}
			return err
		},
		"list":     func() error { _, err := s.List(t.Context(), "ws", "agent", "chat", "owner"); return err },
		"revoke":   func() error { return s.Revoke(t.Context(), "ws", "agent", "chat", g.ID, "owner") },
		"validate": func() error { _, err := s.Validate(t.Context(), g.ID, token); return err },
	} {
		if err := operation(); err == nil || errors.Is(err, ErrDenied) || !strings.Contains(err.Error(), "database is closed") {
			t.Errorf("%s hid storage outage: %v", name, err)
		}
	}
}

func TestCorruptShareTimestampsFailClosed(t *testing.T) {
	for _, column := range []string{"created_at", "expires_at", "revoked_at"} {
		t.Run(column, func(t *testing.T) {
			db := shareTestDB(t)
			s := NewStore(db)
			g, token, err := s.Create(t.Context(), "ws", "agent", "chat", "owner", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			invalid := "malformed"
			if column == "created_at" {
				invalid = "!" // Still satisfies the lexical expires_at > created_at CHECK.
			}
			if _, err := db.Exec(`UPDATE chat_read_shares SET `+column+`=? WHERE id=?`, invalid, g.ID); err != nil {
				t.Fatal(err)
			}
			if list, err := s.List(t.Context(), "ws", "agent", "chat", "owner"); err == nil || list != nil || !strings.Contains(err.Error(), column) {
				t.Fatalf("corrupt timestamp listed as valid: %+v, %v", list, err)
			}
			if _, err := s.Validate(t.Context(), g.ID, token); err == nil {
				t.Fatal("corrupt grant validated")
			}
		})
	}
}

func TestRevokedGrantRemainsVisibleWithoutTokenMaterial(t *testing.T) {
	db := shareTestDB(t)
	s := NewStore(db)
	g, _, err := s.Create(t.Context(), "ws", "agent", "chat", "owner", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(t.Context(), "ws", "agent", "chat", g.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(t.Context(), "ws", "agent", "chat", "owner")
	if err != nil || len(list) != 1 || list[0].RevokedAt == nil || list[0].RevokedAt.Before(g.CreatedAt) {
		t.Fatalf("revocation audit lost: %+v, %v", list, err)
	}
	if _, err := db.Exec(`DROP TABLE chat_read_shares`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(t.Context(), "ws", "agent", "chat", "owner"); err == nil || !strings.Contains(err.Error(), "list chat shares") {
		t.Fatalf("missing grant storage became an empty list: %v", err)
	}
}
