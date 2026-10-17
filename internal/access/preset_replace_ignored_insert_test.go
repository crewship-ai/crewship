package access

import (
	"errors"
	"reflect"
	"testing"
)

func TestPresetReplaceIgnoredManagedInsertReturnsConflict(t *testing.T) {
	for _, action := range []string{"add", "reissue"} {
		t.Run(action, func(t *testing.T) {
			s := fixture(t)
			rights := []Right{{"project", "p2", "read"}}
			if action == "reissue" {
				rights = append(rights, Right{"project", "p1", "list"}, Right{"project", "p1", "read"})
			}
			m := policy(t, s, "h1", rights...)
			before, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.DB.Exec(`CREATE TRIGGER ignore_managed_grant BEFORE INSERT ON access_grants WHEN NEW.member_id='m1' AND NEW.resource_kind='project' AND NEW.project_id='p1' AND NEW.operation='read' BEGIN SELECT RAISE(IGNORE); END`); err != nil {
				t.Fatal(err)
			}
			tx, err := s.DB.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			req := PresetRequest{MembershipID: m.ID, Revision: m.Revision, Resource: PresetResource{Kind: "project", ID: "p1"}, PresetID: "project-levels", Version: 1, Level: "view", Reissue: []Right{}}
			if action == "reissue" {
				req.Reissue = []Right{{"project", "p1", "read"}}
			}
			if _, err := s.ReplacePresetIn(t.Context(), tx, "owner", "h1", "w", req); !errors.Is(err, ErrConflict) {
				t.Fatalf("ignored %s INSERT reported success/non-conflict: %v", action, err)
			}
			// This helper owns no commit. The real transaction owner rolls back errors;
			// a reissue must restore its original ID/issuer/time and unrelated rights.
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			after, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("failed managed writer escaped caller rollback: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}
