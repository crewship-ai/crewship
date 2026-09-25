package groupchat

import (
	"context"
	"testing"
)

func TestSearchPageFiltersBeforePaginationAndRespectsAccess(t *testing.T) {
	s, db, _ := fixture(t)
	ctx := context.Background()
	old, err := s.Create(ctx, "w", "u0", CreateInput{Title: "Školení Find this old channel", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, "w", "u0", CreateInput{Title: "Newer unrelated channel", Kind: "channel"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Create(ctx, "w", "u1", CreateInput{Title: "Find private secret", Kind: "group"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE workspace_conversations SET updated_at='2000-01-01' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	rows, err := s.SearchPage(ctx, "w", "u0", "školení", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != old.ID {
		t.Fatalf("search must find old accessible channel, got %+v", rows)
	}
	rows, err = s.SearchPage(ctx, "w", "u0", "private secret", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("private group leaked")
	}
	rows, err = s.SearchPage(ctx, "w", "u0", "%", 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("wildcard must be literal")
	}
}
