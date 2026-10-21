//go:build linux

package restrictedworkflow

import (
	"errors"
	"strings"
	"testing"
)

// The graph fixture has no archives despite its default HEAD1. Establish legacy
// HEAD0 explicitly before admission or snapshot.
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
