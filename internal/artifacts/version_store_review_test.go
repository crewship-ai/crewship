package artifacts

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReviewVersionStoreRejectsWeakerDurability(t *testing.T) {
	s, k, a, db := versionFixture(t, VersionOptions{})
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA synchronous=NORMAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "must-not-publish"), a); err == nil {
		t.Fatal("published with synchronous=NORMAL")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("durability refusal left a version: %d %v", count, err)
	}
}
func TestReviewVersionStoreRechecksAuthorityAfterPublication(t *testing.T) {
	s, k, _, db := versionFixture(t, VersionOptions{})
	revoked := errors.New("revoked during publication")
	a := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var suspended sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT suspended_at FROM users WHERE id='owner'`).Scan(&suspended); err != nil {
			return "", err
		}
		if suspended.Valid {
			return "", revoked
		}
		return "owner", nil
	}
	// A database-side effect during publication stands in for a delayed expiry or
	// canonical policy cut. It must be observed before the transaction commits.
	if _, err := db.Exec(`CREATE TRIGGER review_revoke_after_version AFTER INSERT ON artifact_versions BEGIN UPDATE users SET suspended_at='revoked' WHERE id='owner'; END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "must-rollback"), a); !errors.Is(err, revoked) {
		t.Fatalf("publication ignored final authority: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("revoked publication survived: %d %v", count, err)
	}
}

func TestReviewVersionStoreRejectsChangedFinalActor(t *testing.T) {
	s, k, _, db := versionFixture(t, VersionOptions{})
	auth := func(ctx context.Context, tx *sql.Tx) (string, error) {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_versions`).Scan(&count)
		if count > 0 {
			return "different-owner", err
		}
		return "owner", err
	}
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "must-rollback"), auth); err == nil {
		t.Fatal("published after actor changed")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("changed actor survived: %d %v", count, err)
	}
}
func TestReviewVersionViewRejectsFinalSelectionDowngrade(t *testing.T) {
	s, k, a, _ := versionFixture(t, VersionOptions{})
	if _, err := s.Apply(t.Context(), k, versionRequest(0, "private"), a); err != nil {
		t.Fatal(err)
	}
	calls := 0
	viewAuth := func(context.Context, *sql.Tx) (string, VersionSelection, error) {
		calls++
		return "owner", VersionSelection{SharedOnly: calls > 1, IncludeHistory: calls == 1}, nil
	}
	if v, err := s.View(t.Context(), k, viewAuth); err == nil || len(v.State.Manifest.Files) != 0 || len(v.Versions) != 0 {
		t.Fatalf("selection downgraded but private snapshot released: %+v %v", v, err)
	}
}
