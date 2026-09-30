package access

import (
	"errors"
	"strings"
	"testing"
)

func projectFileFixture(t *testing.T) (Store, ProjectFileVersion, ProjectFileVersion) {
	t.Helper()
	s := fixture(t)
	if _, err := s.DB.Exec(`UPDATE workspace_members SET role='MANAGER' WHERE user_id IN ('h1','h2')`); err != nil {
		t.Fatal(err)
	}
	policy(t, s, "h1", Right{"agent", "a", "run"}, Right{"project", "p1", "read"}, Right{"project", "p1", "write"})
	policy(t, s, "h2", Right{"agent", "a", "run"}, Right{"project", "p2", "read"}, Right{"project", "p2", "write"})
	one, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: "docs/one.txt"}, []byte("P1_PRIVATE_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.PutProjectFile(t.Context(), "h2", "w", "p2", ProjectFileWrite{Name: "docs/two.txt"}, []byte("P2_PRIVATE_CANARY"))
	if err != nil {
		t.Fatal(err)
	}
	return s, one, two
}

func TestProjectFilesTwoHumansAndImmutableSelection(t *testing.T) {
	s, one, two := projectFileFixture(t)
	files, err := s.ProjectFiles(t.Context(), "h1", "w", "p1")
	if err != nil || len(files) != 1 || files[0].ID != one.ID {
		t.Fatalf("own list: %v %#v", err, files)
	}
	if _, err = s.ProjectFiles(t.Context(), "h1", "w", "p2"); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign list: %v", err)
	}
	if _, _, err = s.ReadProjectFile(t.Context(), "h1", "w", "p1", two.ID); !errors.Is(err, ErrDenied) {
		t.Fatalf("guessed foreign ID: %v", err)
	}
	if _, err = s.ProjectFileRights(t.Context(), "h1", "w", []string{two.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("foreign native selection: %v", err)
	}
	if _, err = s.ProjectFileRights(t.Context(), "h1", "w", []string{one.ID, one.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("duplicate selection: %v", err)
	}
	rights, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{one.ID})
	if err != nil {
		t.Fatal(err)
	}
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BindProjectFileInputs(t.Context(), h, []string{one.ID}); err != nil {
		t.Fatal(err)
	}
	inputs, err := s.FrozenProjectInputs(t.Context(), h)
	if err != nil || len(inputs) != 1 || string(inputs[0].Content) != "P1_PRIVATE_CANARY" {
		t.Fatalf("frozen source: %v %#v", err, inputs)
	}
	if err = s.BindProjectFileInputs(t.Context(), h, []string{one.ID}); !errors.Is(err, ErrConflict) {
		t.Fatalf("selection extended: %v", err)
	}
	if _, err = s.DB.Exec(`UPDATE attempt_project_inputs SET scope='foreign' WHERE attempt_id=?`, a.ID); err == nil {
		t.Fatal("source edge mutated")
	}
	if _, err = s.DB.Exec(`DELETE FROM attempt_project_inputs WHERE attempt_id=?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Resolve(t.Context(), h); !errors.Is(err, ErrDenied) {
		t.Fatalf("deleting source edge preserves launch: %v", err)
	}
}

func TestProjectVersionReplaceRetireAndTransitiveRevocation(t *testing.T) {
	for _, change := range []string{"replace", "retire", "delete-blob", "delete-version", "revoke-grant"} {
		t.Run(change, func(t *testing.T) {
			s, one, _ := projectFileFixture(t)
			rights, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{one.ID})
			if err != nil {
				t.Fatal(err)
			}
			h, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.BindProjectFileInputs(t.Context(), h, []string{one.ID}); err != nil {
				t.Fatal(err)
			}
			if _, err = s.AppendContext(t.Context(), h, ContextAssistant, "derived P1 source canary"); err != nil {
				t.Fatal(err)
			}
			if err = s.CompleteAttempt(t.Context(), h); err != nil {
				t.Fatal(err)
			}
			next, consumer, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.BuildContext(t.Context(), consumer, "next turn"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "replace":
				_, err = s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{FileID: one.FileID, Name: one.Name, ExpectedRevision: one.Revision}, []byte("replacement"))
			case "retire":
				err = s.RetireProjectFile(t.Context(), "h1", "w", "p1", one.FileID, one.Revision)
			case "delete-blob":
				_, err = s.DB.Exec(`DELETE FROM project_file_blobs WHERE version_id=?`, one.ID)
			case "delete-version":
				_, err = s.DB.Exec(`DELETE FROM project_file_versions WHERE id=?`, one.ID)
			case "revoke-grant":
				_, err = s.DB.Exec(`DELETE FROM access_grants WHERE project_id='p1' AND operation='read'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.Resolve(t.Context(), next); !errors.Is(err, ErrDenied) {
				t.Fatalf("derived prepared prompt survived %s: %v", change, err)
			}
			if err = s.CheckContextAttempt(t.Context(), h); !errors.Is(err, ErrDenied) {
				t.Fatalf("historical source survived %s: %v", change, err)
			}
			if change == "replace" || change == "retire" {
				var n int
				if err = s.DB.QueryRow(`SELECT COUNT(*) FROM project_file_versions WHERE id=? AND retired_at IS NOT NULL`, one.ID).Scan(&n); err != nil || n != 1 {
					t.Fatal("source tombstone lost")
				}
				if _, _, err = s.ReadProjectFile(t.Context(), "h1", "w", "p1", one.ID); !errors.Is(err, ErrDenied) {
					t.Fatal("retired source readable")
				}
			}
		})
	}
}

func TestProjectInputBindingMustPrecedeContextAndMatchHostAttempt(t *testing.T) {
	s, one, _ := projectFileFixture(t)
	rights, err := s.ProjectFileRights(t.Context(), "h1", "w", []string{one.ID})
	if err != nil {
		t.Fatal(err)
	}
	h, a, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	forged := a
	forged.Scope = "foreign"
	if err = s.BindProjectFileInputsForAttempt(t.Context(), forged, []string{one.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("forged host scope %v", err)
	}
	if err = s.BindProjectFileInputsForAttempt(t.Context(), a, []string{one.ID}); err != nil {
		t.Fatal(err)
	}
	if files, err := s.FrozenProjectInputsForAttempt(t.Context(), a); err != nil || len(files) != 1 {
		t.Fatalf("host snapshot %v", err)
	}
	if _, err = s.FrozenProjectInputsForAttempt(t.Context(), forged); !errors.Is(err, ErrDenied) {
		t.Fatalf("forged snapshot %v", err)
	}
	if _, err = s.AppendContext(t.Context(), h, ContextUser, "first classified input"); err != nil {
		t.Fatal(err)
	}
	newHandle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", rights)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.AppendContext(t.Context(), newHandle, ContextUser, "context already admitted"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindProjectFileInputs(t.Context(), newHandle, []string{one.ID}); !errors.Is(err, ErrDenied) {
		t.Fatalf("late source binding %v", err)
	}
}

func TestProjectFileRoleCASCapacityAndBytes(t *testing.T) {
	s, one, _ := projectFileFixture(t)
	if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{FileID: one.FileID, Name: one.Name, ExpectedRevision: 99}, []byte("stale")); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale overwrite %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE workspace_members SET role='MEMBER' WHERE user_id='h1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: "write.txt"}, []byte("write")); !errors.Is(err, ErrDenied) {
		t.Fatalf("grant bypasses role %v", err)
	}
	if _, err := s.DB.Exec(`UPDATE workspace_members SET role='MANAGER' WHERE user_id='h1'`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../foreign", ".env", "a/../b", "/absolute", "a\\b"} {
		if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: name}, nil); !errors.Is(err, ErrDenied) {
			t.Fatalf("unsafe name %s %v", name, err)
		}
	}
	content := []byte(strings.Repeat("x", MaxProjectFileBytes))
	for i := 0; i < 23; i++ {
		name := strings.Repeat("a", i+1) + ".bin"
		if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: name}, content); err != nil {
			t.Fatal(err)
		}
	}
	// Base64 storage counts encoded bytes, so 24 full MiB files exceed32MiB.
	if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: "overflow.bin"}, content); err == nil {
		t.Fatal("aggregate capacity not enforced")
	}
	if _, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{FileID: one.FileID, Name: one.Name, ExpectedRevision: 1}, []byte("new")); err != nil {
		t.Fatal("same bounded replacement failed", err)
	}
	if err := s.RetireProjectFile(t.Context(), "h1", "w", "p1", one.FileID, 2); err != nil {
		t.Fatal(err)
	}
	newVersion, err := s.PutProjectFile(t.Context(), "h1", "w", "p1", ProjectFileWrite{Name: one.Name}, []byte("new generation"))
	if err != nil || newVersion.FileID == one.FileID {
		t.Fatalf("new file identity after retirement %v", err)
	}
}
