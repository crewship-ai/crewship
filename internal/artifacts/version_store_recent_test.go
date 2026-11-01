package artifacts

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
)

// versionRowsSpy records the history query and its arguments.
type versionRowsSpy struct {
	inner *sql.Tx
	query string
	args  []any
}

func (s *versionRowsSpy) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	s.query, s.args = query, args
	return s.inner.QueryContext(ctx, query, args...)
}

func TestVersionStoreRecentVersionsIsBoundedInSQL(t *testing.T) {
	s, k, auth, db := versionFixture(t, VersionOptions{})
	for base := int64(0); base < 205; base++ {
		if _, err := s.Apply(t.Context(), k, versionRequest(base, fmt.Sprintf("v%d", base+1)), auth); err != nil {
			t.Fatal(base, err)
		}
	}
	for _, tc := range []struct {
		limit       int
		count       int
		first, last int64
		truncated   bool
	}{{200, 200, 205, 6, true}, {205, 205, 205, 1, false}, {500, 205, 205, 1, false}, {1, 1, 205, 205, true}} {
		t.Run(fmt.Sprint(tc.limit), func(t *testing.T) {
			out, truncated, err := s.RecentVersions(t.Context(), k, tc.limit, auth)
			if err != nil || len(out) != tc.count || truncated != tc.truncated || out[0].Version != tc.first || out[len(out)-1].Version != tc.last {
				t.Fatalf("recent %d: len %d truncated %v err %v", tc.limit, len(out), truncated, err)
			}
			for i := 1; i < len(out); i++ {
				if out[i].Version >= out[i-1].Version {
					t.Fatalf("not newest first at %d", i)
				}
			}
		})
	}
	if _, _, err := s.RecentVersions(t.Context(), k, 0, auth); err == nil {
		t.Fatal("unbounded limit accepted")
	}
	// The bound is in SQL: the store scans at most limit+1 rows, never the history.
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	spy := &versionRowsSpy{inner: tx}
	rows, err := listRecentVersions(t.Context(), spy, k, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 201 || !strings.Contains(spy.query, "ORDER BY version DESC LIMIT ?") || len(spy.args) != 3 || spy.args[2] != 201 {
		t.Fatalf("history query not bounded: %d rows, %q %v", len(rows), spy.query, spy.args)
	}
}
