//go:build linux

package restrictedworkflow

import (
	"errors"
	"strings"
	"testing"
)

// The existing graph helper inserts fresh routine rows directly. Native v79
// defaults HEAD to 1 even though that helper creates no archive. State the
// intended legacy HEAD0 fixture explicitly before any admission or snapshot.
func graphGuardFixture(t *testing.T) *Service {
	t.Helper()
	s, _ := graphFixture(t)
	var archives int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pipeline_versions WHERE pipeline_id IN ('routine','nested')`).Scan(&archives); err != nil || archives != 0 {
		t.Fatalf("fresh legacy fixture has archive history: %d %v", archives, err)
	}
	result, err := s.db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=0 WHERE id IN ('routine','nested') AND workspace_id='w'`)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 2 {
		t.Fatalf("legacy HEAD0 setup: rows=%d error=%v", n, err)
	}
	return s
}

// Insert a fresh archive rather than modifying immutable publication metadata.
// This is a negative storage marker, not a successful publication/admission.
func graphGuardArchive(t *testing.T, s *Service, id, fingerprint string, manifest any) {
	t.Helper()
	result, err := s.db.ExecContext(t.Context(), `INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint,publication_authority_json)
SELECT id||'-guard-version',id,1,definition_json,definition_hash,'user','h1',?,? FROM pipelines WHERE id=? AND workspace_id='w'`, fingerprint, manifest, id)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		t.Fatalf("archive setup: rows=%d error=%v", n, err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	var selected, kind, bodyMatch int
	if err := s.db.QueryRowContext(t.Context(), `SELECT p.head_version,typeof(v.authority_fingerprint)='text',p.definition_json=v.definition_json AND p.definition_hash=v.definition_hash FROM pipelines p JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version WHERE p.id=?`, id).Scan(&selected, &kind, &bodyMatch); err != nil || selected != 1 || kind != 1 || bodyMatch != 1 {
		t.Fatalf("archive setup: selected=%d text=%d body=%d error=%v", selected, kind, bodyMatch, err)
	}
}

func graphGuardLegacyJob(t *testing.T, s *Service) job {
	t.Helper()
	if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); err != nil {
		t.Fatalf("legacy graph compilation calibration: %v", err)
	}
	receipt, err := s.AdmitManual(t.Context(), "h1", "w", "private-work", map[string]any{"task": "synthetic graph guard input"}, "", 0)
	if err != nil {
		t.Fatalf("legacy admission calibration: %v", err)
	}
	j, err := s.load(t.Context(), receipt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.checkGraph(t.Context(), s.db, j); err != nil {
		t.Fatalf("persisted frozen legacy graph calibration: %v", err)
	}
	return j
}

func TestRestrictedGraphPublicationBoundary(t *testing.T) {
	for _, target := range []string{"routine", "nested"} {
		t.Run(target, func(t *testing.T) {
			s := graphGuardFixture(t)
			j := graphGuardLegacyJob(t, s)
			graphGuardArchive(t, s, target, strings.Repeat("a", 64), `{}`)
			t.Run("compile", func(t *testing.T) {
				if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); !errors.Is(err, ErrDenied) {
					t.Fatalf("published %s reached invoker graph compilation: %v", target, err)
				}
			})
			t.Run("persisted frozen graph", func(t *testing.T) {
				if err := s.checkGraph(t.Context(), s.db, j); !errors.Is(err, ErrDenied) {
					t.Fatalf("published %s retained invoker frozen graph: %v", target, err)
				}
			})
		})
	}
}

func TestRestrictedGraphPublicationGuardLegacyHeadAndMissingArchive(t *testing.T) {
	s := graphGuardFixture(t)
	var headZero, archives int
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pipelines WHERE id IN ('routine','nested') AND head_version=0`).Scan(&headZero); err != nil || headZero != 2 {
		t.Fatalf("HEAD0 calibration: count=%d error=%v", headZero, err)
	}
	if err := s.db.QueryRowContext(t.Context(), `SELECT count(*) FROM pipeline_versions WHERE pipeline_id IN ('routine','nested')`).Scan(&archives); err != nil || archives != 0 {
		t.Fatalf("legacy archive calibration: count=%d error=%v", archives, err)
	}
	j := graphGuardLegacyJob(t, s)
	graphGuardArchive(t, s, "routine", "legacy-trusted:v1", nil)
	if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); err != nil {
		t.Fatalf("selected legacy archive denied: %v", err)
	}
	if err := s.checkGraph(t.Context(), s.db, j); err != nil {
		t.Fatalf("selected legacy archive invalidated unchanged frozen graph: %v", err)
	}
	if _, err := s.db.ExecContext(t.Context(), `UPDATE pipelines SET head_version=2 WHERE id='routine'`); err != nil {
		t.Fatal(err)
	}
	t.Run("compile", func(t *testing.T) {
		if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); !errors.Is(err, ErrDenied) {
			t.Fatalf("missing positive HEAD archive became legacy: %v", err)
		}
	})
	t.Run("persisted frozen graph", func(t *testing.T) {
		if err := s.checkGraph(t.Context(), s.db, j); !errors.Is(err, ErrDenied) {
			t.Fatalf("missing positive HEAD archive retained frozen graph: %v", err)
		}
	})
}

func TestRestrictedGraphPublicationGuardStorageError(t *testing.T) {
	s := graphGuardFixture(t)
	j := graphGuardLegacyJob(t, s)
	graphGuardArchive(t, s, "routine", "legacy-trusted:v1", nil)
	if _, err := s.compileGraph(t.Context(), "h1", "w", "routine"); err != nil {
		t.Fatalf("legacy archive calibration: %v", err)
	}
	if err := s.checkGraph(t.Context(), s.db, j); err != nil {
		t.Fatalf("legacy frozen archive calibration: %v", err)
	}
	// The real archive lookup now fails operationally, rather than returning
	// a missing selected row. Do not manufacture a permission refusal.
	if _, err := s.db.ExecContext(t.Context(), `ALTER TABLE pipeline_versions RENAME TO graph_guard_unavailable_versions`); err != nil {
		t.Fatal(err)
	}
	t.Run("compile", func(t *testing.T) {
		_, err := s.compileGraph(t.Context(), "h1", "w", "routine")
		if err == nil || errors.Is(err, ErrDenied) || errors.Is(err, ErrUnsupported) {
			t.Fatalf("publication storage failure lost its class: %v", err)
		}
	})
	t.Run("persisted frozen graph", func(t *testing.T) {
		err := s.checkGraph(t.Context(), s.db, j)
		if err == nil || errors.Is(err, ErrDenied) || errors.Is(err, ErrUnsupported) {
			t.Fatalf("frozen publication storage failure lost its class: %v", err)
		}
	})
}
