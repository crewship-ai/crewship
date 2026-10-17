package access

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
)

func publicationTx(t *testing.T, s Store) *sql.Tx {
	t.Helper()
	tx, err := s.DB.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}

func TestPublicationGrantCaptureExactIdentity(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	var grant string
	if err := s.DB.QueryRow(`SELECT id FROM access_grants WHERE member_id='m1'`).Scan(&grant); err != nil {
		t.Fatal(err)
	}
	tx := publicationTx(t, s)
	binding, err := s.CapturePublicationGrantIn(t.Context(), tx, "h1", "w", right)
	if err != nil {
		t.Fatalf("live selected publication dependency must capture exact provenance: %v", err)
	}
	want := PublicationGrantBinding{WorkspaceID: "w", PublisherID: "h1", PublisherMembershipID: "m1", Right: right, GrantID: grant, IssuerID: "owner", IssuerMembershipID: "mo"}
	if binding != want {
		t.Fatalf("binding = %+v, want %+v", binding, want)
	}
	if err := s.ResolvePublicationGrantIn(t.Context(), tx, binding); err != nil {
		t.Fatal(err)
	}
}

func capturePublication(t *testing.T, s Store, right Right) PublicationGrantBinding {
	t.Helper()
	tx := publicationTx(t, s)
	defer func() { _ = tx.Rollback() }()
	binding, err := s.CapturePublicationGrantIn(t.Context(), tx, "h1", "w", right)
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func resolvePublication(t *testing.T, s Store, binding PublicationGrantBinding) error {
	t.Helper()
	tx := publicationTx(t, s)
	defer func() { _ = tx.Rollback() }()
	return s.ResolvePublicationGrantIn(t.Context(), tx, binding)
}

func publicationExec(t *testing.T, s Store, query string) {
	t.Helper()
	if _, err := s.DB.ExecContext(t.Context(), query); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationGrantSameIssuerRecoveryAllowsNewInvocationOnly(t *testing.T) {
	for _, change := range []struct{ name, lose, restore string }{
		{"role", `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`, `UPDATE workspace_members SET role='OWNER' WHERE id='mo'`},
		{"suspension", `UPDATE users SET suspended_at='2026-10-16T00:00:00Z' WHERE id='owner'`, `UPDATE users SET suspended_at=NULL WHERE id='owner'`},
	} {
		t.Run(change.name, func(t *testing.T) {
			s := fixture(t)
			right := Right{"agent", "a", "run"}
			policy(t, s, "h1", right)
			binding := capturePublication(t, s, right)
			old, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			publicationExec(t, s, change.lose)
			if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
				t.Fatalf("issuer loss: %v", err)
			}
			if _, err := s.Resolve(t.Context(), old); !errors.Is(err, ErrDenied) {
				t.Fatalf("old attempt after loss: %v", err)
			}
			publicationExec(t, s, change.restore)
			if got := capturePublication(t, s, right); got != binding {
				t.Fatalf("same grants/member acquired another identity: %+v", got)
			}
			if err := resolvePublication(t, s, binding); err != nil {
				t.Fatalf("new resolution after recovery: %v", err)
			}
			if _, err := s.Resolve(t.Context(), old); !errors.Is(err, ErrDenied) {
				t.Fatalf("old attempt revived: %v", err)
			}
			fresh, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Resolve(t.Context(), fresh); err != nil {
				t.Fatalf("fresh attempt: %v", err)
			}
		})
	}
}

func TestPublicationGrantUnrelatedSavePreservesExactIdentity(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	before := policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	after := policy(t, s, "h1", right, Right{"project", "p1", "read"})
	if after.Revision <= before.Revision {
		t.Fatal("control: policy revision did not advance")
	}
	if got := capturePublication(t, s, right); got != binding {
		t.Fatalf("unrelated save changed selected provenance: %+v", got)
	}
	if err := resolvePublication(t, s, binding); err != nil {
		t.Fatal(err)
	}
	// A second administrator's unrelated issuer loss is not this binding's loss.
	publicationExec(t, s, `UPDATE workspace_members SET role='ADMIN' WHERE id='m2'`)
	publicationExec(t, s, `INSERT INTO users(id,email) VALUES('h3','h3@publication.test')`)
	publicationExec(t, s, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m3','w','h3','VIEWER')`)
	m, err := s.Membership(t.Context(), "h3", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), "h2", "h3", "w", "restricted", m, []Right{{"project", "p2", "read"}}); err != nil {
		t.Fatal(err)
	}
	publicationExec(t, s, `UPDATE workspace_members SET role='MEMBER' WHERE id='m2'`)
	if err := resolvePublication(t, s, binding); err != nil {
		t.Fatalf("unrelated issuer changed publication: %v", err)
	}
}

func TestPublicationGrantExplicitReissueCannotSubstitute(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	publicationExec(t, s, `UPDATE workspace_members SET role='ADMIN' WHERE id='m2'`)
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), "h2", "h1", "w", "restricted", m, []Right{right}, right); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(t.Context(), "h1", "w", right); err != nil {
		t.Fatalf("control: current share missing: %v", err)
	}
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("replacement accepted: %v", err)
	}
	current := capturePublication(t, s, right)
	if current.GrantID == binding.GrantID || current.IssuerID != "h2" || current.IssuerMembershipID != "m2" {
		t.Fatalf("wrong reissue identity: %+v", current)
	}
	if err := resolvePublication(t, s, current); err != nil {
		t.Fatal(err)
	}
}

func TestPublicationGrantRemovedAndReaddedRightCannotSubstitute(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	policy(t, s, "h1")
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed right: %v", err)
	}
	policy(t, s, "h1", right)
	current := capturePublication(t, s, right)
	if current.GrantID == binding.GrantID || current.IssuerID != binding.IssuerID {
		t.Fatal("control: expected replacement grant by original issuer")
	}
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("readded right revived old binding: %v", err)
	}
}

func TestPublicationGrantIssuerMembershipReplacementCannotSubstitute(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	publicationExec(t, s, `DELETE FROM workspace_members WHERE id='mo'`)
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("removed issuer: %v", err)
	}
	publicationExec(t, s, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('mo-new','w','owner','OWNER')`)
	if err := s.Check(t.Context(), "h1", "w", right); err != nil {
		t.Fatalf("control: current issuer is live: %v", err)
	}
	current := capturePublication(t, s, right)
	if current.GrantID != binding.GrantID || current.IssuerMembershipID != "mo-new" {
		t.Fatalf("control: retained grant should bind new membership: %+v", current)
	}
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("issuer rejoin revived old binding: %v", err)
	}
}

func TestPublicationGrantPublisherMembershipReplacementCannotSubstitute(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	publicationExec(t, s, `DELETE FROM workspace_members WHERE id='m1'`)
	publicationExec(t, s, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('m1-new','w','h1','MEMBER')`)
	policy(t, s, "h1", right)
	current := capturePublication(t, s, right)
	if current.PublisherMembershipID != "m1-new" {
		t.Fatal("control: publisher membership was not replaced")
	}
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("publisher rejoin revived old binding: %v", err)
	}
}

func TestPublicationGrantTrustedModeDoesNotBypassOriginalShare(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	m, err := s.Membership(t.Context(), "h1", "w")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Replace(t.Context(), "owner", "h1", "w", "trusted", m, nil); err != nil {
		t.Fatal(err)
	}
	// Characterize and preserve Check's intended trusted RBAC behavior.
	if err := s.Check(t.Context(), "h1", "w", right); err != nil {
		t.Fatalf("existing trusted behavior changed: %v", err)
	}
	if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("trusted mode widened bounded publication: %v", err)
	}
	tx := publicationTx(t, s)
	if _, err := s.CapturePublicationGrantIn(t.Context(), tx, "h1", "w", right); !errors.Is(err, ErrDenied) {
		t.Fatalf("trusted capture invented explicit share: %v", err)
	}
}

func TestPublicationGrantChecksLiveResourceAndAccount(t *testing.T) {
	for _, change := range []struct{ name, query string }{
		{"publisher suspension", `UPDATE users SET suspended_at='' WHERE id='h1'`},
		{"resource retirement", `UPDATE agents SET deleted_at='2026-10-16T00:00:00Z' WHERE id='a'`},
		{"resource scope", `UPDATE agents SET workspace_id='other' WHERE id='a'`},
		{"workspace retirement", `UPDATE workspaces SET deleted_at='2026-10-16T00:00:00Z' WHERE id='w'`},
	} {
		t.Run(change.name, func(t *testing.T) {
			s := fixture(t)
			right := Right{"agent", "a", "run"}
			policy(t, s, "h1", right)
			binding := capturePublication(t, s, right)
			publicationExec(t, s, change.query)
			if err := resolvePublication(t, s, binding); !errors.Is(err, ErrDenied) {
				t.Fatalf("unavailable dependency allowed: %v", err)
			}
		})
	}
}

func TestPublicationGrantUsesCallerTransactionAndDoesNotWrite(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	tx := publicationTx(t, s)
	if _, err := tx.Exec(`DELETE FROM access_grants WHERE id=?`, binding.GrantID); err != nil {
		t.Fatal(err)
	}
	if err := s.ResolvePublicationGrantIn(t.Context(), tx, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("ignored caller's snapshot: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := resolvePublication(t, s, binding); err != nil {
		t.Fatalf("read-only resolution changed persisted grants: %v", err)
	}
}

func TestPublicationGrantRejectsIncompleteTamperedAndForeignBindings(t *testing.T) {
	s := fixture(t)
	right := Right{"agent", "a", "run"}
	policy(t, s, "h1", right)
	binding := capturePublication(t, s, right)
	for _, mutate := range []func(*PublicationGrantBinding){
		func(b *PublicationGrantBinding) { b.WorkspaceID = "other" },
		func(b *PublicationGrantBinding) { b.PublisherID = "h2" },
		func(b *PublicationGrantBinding) { b.PublisherMembershipID = "" },
		func(b *PublicationGrantBinding) { b.GrantID = "other-grant" },
		func(b *PublicationGrantBinding) { b.IssuerID = "h2" },
		func(b *PublicationGrantBinding) { b.IssuerMembershipID = "other-member" },
		func(b *PublicationGrantBinding) { b.Right = Right{"agent", "b", "run"} },
	} {
		altered := binding
		mutate(&altered)
		if err := resolvePublication(t, s, altered); !errors.Is(err, ErrDenied) {
			t.Fatalf("tampered binding accepted: %+v %v", altered, err)
		}
	}
	tx := publicationTx(t, s)
	for _, r := range []Right{{"project", "foreign", "read"}, {"routine", "missing", "run"}, {"routine", "missing", "publish"}, {"agent", "a", "unknown"}} {
		if _, err := s.CapturePublicationGrantIn(t.Context(), tx, "h1", "w", r); !errors.Is(err, ErrDenied) {
			t.Fatalf("invalid selection %+v: %v", r, err)
		}
	}
	if _, err := s.CapturePublicationGrantIn(t.Context(), nil, "h1", "w", right); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing snapshot: %v", err)
	}
	if err := s.ResolvePublicationGrantIn(t.Context(), nil, binding); !errors.Is(err, ErrDenied) {
		t.Fatalf("missing resolution snapshot: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CapturePublicationGrantIn(t.Context(), tx, "h1", "w", right); err == nil {
		t.Fatal("closed snapshot succeeded")
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Fatalf("host provenance leaked through JSON: %s", raw)
	}
}
