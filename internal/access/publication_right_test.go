package access

import (
	"errors"
	"testing"
)

func TestPublicationRightRequiresExplicitEditWithoutImplyingViewOrRun(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('publication','w','Publication','publication','{}','hash','active')`); err != nil {
		t.Fatal(err)
	}
	publish := Right{Kind: "routine", ID: "publication", Operation: "publish"}
	edit := Right{Kind: "routine", ID: "publication", Operation: "edit"}
	view := Right{Kind: "routine", ID: "publication", Operation: "view"}
	// A complete explicit policy may store Edit+Publish without manufacturing
	// View or Run. Effective publication must still satisfy the View gate.
	m := policy(t, s, "h1", edit, publish)
	if err := ValidateRights(t.Context(), s.DB, "w", []Right{edit, publish}); err != nil {
		t.Fatal("valid explicit Edit+Publish rejected", err)
	}
	for _, operation := range []string{"view", "run", "publish"} {
		if err := s.Check(t.Context(), "h1", "w", Right{Kind: "routine", ID: "publication", Operation: operation}); !errors.Is(err, ErrDenied) {
			t.Fatalf("missing View or Run was implied for %s: %v", operation, err)
		}
	}
	for _, invalid := range [][]Right{{publish}, {view, publish}} {
		if err := ValidateRights(t.Context(), s.DB, "w", invalid); !errors.Is(err, ErrDenied) {
			t.Fatalf("invitation accepted Publish without Edit: %v", err)
		}
		if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, invalid); !errors.Is(err, ErrDenied) {
			t.Fatalf("policy replacement accepted Publish without Edit: %v", err)
		}
		current, err := s.Membership(t.Context(), "h1", "w")
		if err != nil || current != m {
			t.Fatal("refused policy changed membership revision", current, err)
		}
	}
	policy(t, s, "h1", view, edit, publish)
	if err := s.Check(t.Context(), "h1", "w", publish); err != nil {
		t.Fatal("explicit current publication rights rejected", err)
	}
	if err := s.Check(t.Context(), "h1", "w", Right{Kind: "routine", ID: "publication", Operation: "run"}); !errors.Is(err, ErrDenied) {
		t.Fatal("publication implied root Run", err)
	}
	// Removing Edit through another mutation path immediately makes even a
	// retained Publish row ineffective; a later grant is not inherited.
	if _, err := s.DB.Exec(`DELETE FROM access_grants WHERE member_id='m1' AND routine_id='publication' AND operation='edit'`); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context(), "h1", "w", publish); !errors.Is(err, ErrDenied) {
		t.Fatal("publication survived loss of Edit", err)
	}
}

func TestPublicationRightRechecksCurrentDependencies(t *testing.T) {
	for name, mutation := range map[string]string{
		"issuer-role":                     `UPDATE workspace_members SET role='VIEWER' WHERE id='mo'`,
		"global-administrator-constraint": `INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('elsewhere','other','owner','VIEWER','restricted')`,
		"publisher-suspended":             `UPDATE users SET suspended_at='later' WHERE id='h1'`,
		"routine-deleted":                 `UPDATE pipelines SET deleted_at='later' WHERE id='publication'`,
		"edit-removed":                    `DELETE FROM access_grants WHERE member_id='m1' AND operation='edit'`,
		"view-removed":                    `DELETE FROM access_grants WHERE member_id='m1' AND operation='view'`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`INSERT INTO pipelines(id,workspace_id,name,slug,definition_json,definition_hash,status) VALUES('publication','w','Publication','publication','{}','hash','active')`); err != nil {
				t.Fatal(err)
			}
			right := Right{"routine", "publication", "publish"}
			policy(t, s, "h1", Right{"routine", "publication", "view"}, Right{"routine", "publication", "edit"}, right)
			if err := s.Check(t.Context(), "h1", "w", right); err != nil {
				t.Fatal("healthy explicit grant", err)
			}
			if err := s.Check(t.Context(), "owner", "w", right); !errors.Is(err, ErrDenied) {
				t.Fatal("trusted mode implied Publish", err)
			}
			if name == "global-administrator-constraint" {
				// Native schema forbids a Guest membership while this issuer
				// remains an administrator; do not fabricate an illegal ceiling.
				if _, err := s.DB.Exec(mutation); err == nil {
					t.Fatal("schema accepted Guest administrator")
				}
				if err := s.Check(t.Context(), "h1", "w", right); err != nil {
					t.Fatal("refused mutation changed existing authority", err)
				}
				return
			}
			if _, err := s.DB.Exec(mutation); err != nil {
				t.Fatal(err)
			}
			if err := s.Check(t.Context(), "h1", "w", right); !errors.Is(err, ErrDenied) {
				t.Fatal("Publish survived standing loss", err)
			}
		})
	}
}
