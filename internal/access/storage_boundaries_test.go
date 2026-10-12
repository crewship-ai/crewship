package access

import (
	"context"
	"errors"
	"testing"
)

// An unavailable authority store must never turn into a permissive fallback.
// Cancellation also must not leave a partially admitted attempt behind.
func TestAuthorityStorageUnavailable(t *testing.T) {
	operations := []struct {
		name string
		call func(context.Context, Store) error
	}{
		{"membership", func(ctx context.Context, s Store) error { _, err := s.Membership(ctx, "h1", "w"); return err }},
		{"check", func(ctx context.Context, s Store) error { return s.Check(ctx, "h1", "w", Right{"agent", "a", "run"}) }},
		{"policy", func(ctx context.Context, s Store) error { _, err := s.Policy(ctx, "owner", "h1", "w"); return err }},
		{"replace", func(ctx context.Context, s Store) error {
			_, err := s.Replace(ctx, "owner", "h1", "w", "restricted", Membership{}, nil)
			return err
		}},
		{"admit", func(ctx context.Context, s Store) error {
			_, _, err := s.Admit(ctx, "h1", "w", "a", "c1", "", nil)
			return err
		}},
		{"resolve", func(ctx context.Context, s Store) error { _, err := s.Resolve(ctx, "handle"); return err }},
		{"revoke", func(ctx context.Context, s Store) error { return s.RevokeAttempt(ctx, "handle") }},
		{"complete", func(ctx context.Context, s Store) error { return s.CompleteAttempt(ctx, "handle") }},
		{"append", func(ctx context.Context, s Store) error {
			_, err := s.AppendContext(ctx, "handle", ContextUser, "text")
			return err
		}},
		{"entries", func(ctx context.Context, s Store) error { _, err := s.ContextEntries(ctx, Attempt{}); return err }},
		{"chat entries", func(ctx context.Context, s Store) error {
			_, err := s.ContextEntriesForChat(ctx, "h1", "w", "a", "c1")
			return err
		}},
		{"context proof", func(ctx context.Context, s Store) error { return s.CheckContextAttempt(ctx, "handle") }},
		{"workflow proof", func(ctx context.Context, s Store) error { return s.CheckWorkflowContextAttempt(ctx, "handle", "job") }},
		{"delegation", func(ctx context.Context, s Store) error {
			_, err := s.ImportDelegatedContext(ctx, "parent", "child", []string{"source"})
			return err
		}},
		{"file write", func(ctx context.Context, s Store) error {
			_, err := s.SaveFile(ctx, "handle", "result.txt", []byte("output"))
			return err
		}},
		{"file list", func(ctx context.Context, s Store) error {
			_, err := s.FilesForChat(ctx, "h1", "w", "a", "c1")
			return err
		}},
		{"file read", func(ctx context.Context, s Store) error {
			_, _, err := s.ReadFileForChat(ctx, "h1", "w", "a", "c1", "file")
			return err
		}},
		{"note write", func(ctx context.Context, s Store) error {
			_, err := s.SaveNote(ctx, "h1", "w", "a", "c1", "note")
			return err
		}},
		{"note delete", func(ctx context.Context, s Store) error { return s.DeleteNote(ctx, "h1", "w", "a", "c1", "note") }},
		{"outcome write", func(ctx context.Context, s Store) error { return s.RecordOutcome(ctx, "handle", "failed") }},
		{"outcome list", func(ctx context.Context, s Store) error {
			_, err := s.OutcomesForChat(ctx, "h1", "w", "c1")
			return err
		}},
		{"project file write", func(ctx context.Context, s Store) error {
			_, err := s.PutProjectFile(ctx, "h1", "w", "p1", ProjectFileWrite{Name: "result.txt"}, []byte("result"))
			return err
		}},
		{"project file retire", func(ctx context.Context, s Store) error { return s.RetireProjectFile(ctx, "h1", "w", "p1", "file", 1) }},
		{"project file read", func(ctx context.Context, s Store) error {
			_, _, err := s.ReadProjectFile(ctx, "h1", "w", "p1", "file")
			return err
		}},
		{"project file list", func(ctx context.Context, s Store) error { _, err := s.ProjectFiles(ctx, "h1", "w", "p1"); return err }},
		{"project file bind", func(ctx context.Context, s Store) error {
			return s.BindProjectFileInputs(ctx, "handle", []string{"file"})
		}},
		{"project materialize", func(ctx context.Context, s Store) error { _, err := s.FrozenProjectInputs(ctx, "handle"); return err }},
	}
	s := fixture(t)
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			if err := op.call(t.Context(), Store{}); !errors.Is(err, ErrDenied) {
				t.Fatalf("unavailable store did not deny: %v", err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := op.call(ctx, s); err == nil {
				t.Fatal("canceled authority operation succeeded")
			}
		})
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM access_attempts`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("canceled operations admitted work: %d, %v", count, err)
	}
}

func TestAuthorityInputBounds(t *testing.T) {
	s := fixture(t)
	policy(t, s, "h1", Right{"agent", "a", "run"})
	for _, call := range []func() error{
		func() error {
			_, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", make([]Right, 257))
			return err
		},
		func() error {
			_, _, err := s.AdmitWorkflowContinuation(t.Context(), "h1", "w", "a", "c1", "", nil)
			return err
		},
		func() error {
			_, err := s.DeriveContext(t.Context(), "handle", ContextHistory, []string{"source"}, "text")
			return err
		},
		func() error { _, err := s.DeriveContext(t.Context(), "handle", ContextMemory, nil, "text"); return err },
		func() error {
			_, err := s.DeriveContext(t.Context(), "handle", ContextMemory, make([]string, 65), "text")
			return err
		},
		func() error { _, err := s.AppendContext(t.Context(), "handle", ContextUser, ""); return err },
		func() error { _, err := s.ImportDelegatedContext(t.Context(), "parent", "child", nil); return err },
		func() error {
			_, err := s.ImportDelegatedContext(t.Context(), "parent", "child", make([]string, 65))
			return err
		},
		func() error { _, err := s.SaveNote(t.Context(), "h1", "w", "a", "c1", ""); return err },
		func() error { return s.RecordOutcome(t.Context(), "handle", "running") },
		func() error { return s.CheckWorkflowContextAttempt(t.Context(), "handle", "") },
		func() error { _, err := s.ProjectFileRights(t.Context(), "h1", "w", make([]string, 17)); return err },
		func() error { _, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{""}); return err },
	} {
		if err := call(); !errors.Is(err, ErrDenied) {
			t.Errorf("invalid authority input accepted: %v", err)
		}
	}
}

func TestFailedNoteNeverLeavesExecutableAuthority(t *testing.T) {
	for _, stage := range []struct{ name, trigger string }{
		{"source", `CREATE TRIGGER inject_note_failure BEFORE INSERT ON access_context WHEN NEW.kind='history' BEGIN SELECT RAISE(ABORT,'injected source storage failure'); END`},
		{"derivation", `CREATE TRIGGER inject_note_failure BEFORE INSERT ON access_context WHEN NEW.kind='memory' BEGIN SELECT RAISE(ABORT,'injected derived storage failure'); END`},
		{"completion", `CREATE TRIGGER inject_note_failure BEFORE UPDATE OF completed_at ON access_attempts WHEN NEW.completed_at IS NOT NULL BEGIN SELECT RAISE(ABORT,'injected completion storage failure'); END`},
	} {
		t.Run(stage.name, func(t *testing.T) {
			s := fixture(t)
			policy(t, s, "h1", Right{"agent", "a", "chat"})
			if _, err := s.DB.Exec(stage.trigger); err != nil {
				t.Fatal(err)
			}
			note, err := s.SaveNote(t.Context(), "h1", "w", "a", "c1", "PRIVATE_NOTE")
			if err == nil || note.ID != "" {
				t.Fatalf("failed write returned a note: %#v, %v", note, err)
			}
			var live int
			if err := s.DB.QueryRow(`SELECT count(*) FROM access_attempts WHERE revoked_at IS NULL AND completed_at IS NULL`).Scan(&live); err != nil || live != 0 {
				t.Fatalf("failed note left authority: %d, %v", live, err)
			}
			entries, err := s.ContextEntriesForChat(t.Context(), "h1", "w", "a", "c1")
			if err != nil || len(entries) != 0 {
				t.Fatalf("partial note became visible: %#v, %v", entries, err)
			}
		})
	}
}

func TestFileListContainsOnlyCurrentlyAuthorizedOutputs(t *testing.T) {
	s := fixture(t)
	rights := []Right{{"agent", "a", "run"}, {"agent", "a", "chat"}, {"project", "p1", "read"}}
	policy(t, s, "h1", rights...)
	broad, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	hidden, err := s.SaveFile(t.Context(), broad, "broad.txt", []byte("BROAD_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	narrow, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	visible, err := s.SaveFile(t.Context(), narrow, "visible.txt", []byte("VISIBLE_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAttempt(t.Context(), broad); err != nil {
		t.Fatal(err)
	}
	rows, err := s.FilesForChat(t.Context(), "h1", "w", "a", "c1")
	if err != nil || len(rows) != 1 || rows[0].ID != visible.ID {
		t.Fatalf("wrong file projection: %#v, %v (revoked %s)", rows, err, hidden.ID)
	}
	if _, err := s.FilesForChat(t.Context(), "h2", "w", "a", "c1"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign audience: %v", err)
	}
}

func TestPolicyReplacementRollsBackOnStorageFailure(t *testing.T) {
	for _, stage := range []struct{ name, trigger string }{
		{"remove old grants", `CREATE TRIGGER inject_policy_failure BEFORE DELETE ON access_grants BEGIN SELECT RAISE(ABORT,'injected delete failure'); END`},
		{"insert new grants", `CREATE TRIGGER inject_policy_failure BEFORE INSERT ON access_grants BEGIN SELECT RAISE(ABORT,'injected insert failure'); END`},
		{"revision", `CREATE TRIGGER inject_policy_failure BEFORE UPDATE OF access_revision ON workspace_members BEGIN SELECT RAISE(ABORT,'injected revision failure'); END`},
	} {
		t.Run(stage.name, func(t *testing.T) {
			s := fixture(t)
			original := Right{"agent", "a", "run"}
			m := policy(t, s, "h1", original)
			if _, err := s.DB.Exec(stage.trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []Right{{"agent", "b", "run"}}); err == nil {
				t.Fatal("failed replacement accepted")
			}
			after, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil || after.Membership != m || len(after.Rights) != 1 || after.Rights[0] != original {
				t.Fatalf("partial replacement persisted: %#v, %v", after, err)
			}
		})
	}
}
