package mooring

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestReviewNativeStoreValidatorCannotRewriteCandidate(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"v":2}`)
	s.config.Validate = func(_ context.Context, candidate []byte) error {
		copy(candidate, []byte(`{"v":9}`))
		copy(original, []byte(`{"v":8}`))
		return nil
	}
	if _, err = s.Save(t.Context(), l, 1, original); err != nil {
		t.Fatal(err)
	}
	state, err := s.Read(t.Context(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(state.Manifest.Files["document.json"].Content, []byte(`{"v":2}`)) {
		t.Fatal("validator or caller mutation replaced checked candidate")
	}
}

func TestReviewNativeStorePrincipalChangeDuringValidationCannotPublish(t *testing.T) {
	for _, operation := range []string{"initialize", "save", "restore"} {
		t.Run(operation, func(t *testing.T) {
			s, _, db, _, scope, p := nativeStoreFixture(t)
			var l EditLease
			var err error
			expectedCount := 0
			if operation != "initialize" {
				l, _, err = s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
				if err != nil {
					t.Fatal(err)
				}
				expectedCount = 1
			}
			bound := p
			s.config.Authorize = func(context.Context, *sql.Tx, EditLeaseScope, string) (EditLeasePrincipal, error) { return bound, nil }
			s.config.Validate = func(context.Context, []byte) error { bound.SessionID = "different-live-connection"; return nil }
			switch operation {
			case "initialize":
				_, _, err = s.Initialize(t.Context(), scope, p, []byte(`{"v":2}`))
			case "save":
				_, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`))
			case "restore":
				_, err = s.Restore(t.Context(), l, 1, 1)
			}
			if !errors.Is(err, ErrEditLeaseDenied) {
				t.Fatalf("different connection published %s: %v", operation, err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM artifact_versions`).Scan(&count); err != nil || count != expectedCount {
				t.Fatalf("history changed: %d %v", count, err)
			}
		})
	}
}

func TestReviewNativeStoreEnginePinMismatchRejectsReadSaveRestore(t *testing.T) {
	s, _, _, _, scope, p := nativeStoreFixture(t)
	l, _, err := s.Initialize(t.Context(), scope, p, []byte(`{"v":1}`))
	if err != nil {
		t.Fatal(err)
	}
	s.config.EngineSHA256 = strings.Repeat("d", 64)
	s.config.Validate = func(context.Context, []byte) error { t.Fatal("mismatched engine was invoked"); return nil }
	if _, err = s.Read(t.Context(), scope); !errors.Is(err, ErrNativeProfile) {
		t.Fatal(err)
	}
	if _, err = s.Save(t.Context(), l, 1, []byte(`{"v":2}`)); !errors.Is(err, ErrNativeProfile) {
		t.Fatal(err)
	}
	if _, err = s.Restore(t.Context(), l, 1, 1); !errors.Is(err, ErrNativeProfile) {
		t.Fatal(err)
	}
}
