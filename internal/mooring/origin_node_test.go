//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package mooring

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The checkpoint decoder and authority below are explicitly MOCK application
// fixtures. These tests qualify persistence/fencing, never native issuance.
func nodeFixture(t *testing.T) (*BatchStore, OwnerLease, OriginNodeConfig) {
	t.Helper()
	owners, db, _, key := ownerFixture(t)
	key.ApplicationID = "wordcraft"
	owner, err := owners.Acquire(t.Context(), key, "node-runtime")
	if err != nil {
		t.Fatal(err)
	}
	store := NewBatchStore(db.DB, BatchStoreConfig{Enabled: true, Owners: owners, Authority: func(context.Context, *sql.Tx, BatchAccess) error { return nil }, Limits: DefaultBatchLimits(), Targets: &TargetStoreConfig{Enabled: true, Adapter: &targetTestAdapter{}, Limits: DefaultBatchTargetLimits()}, CheckpointIdentity: &BatchCheckpointIdentityConfig{Enabled: true, Adapter: &checkpointTestDescriber{describe: func(context.Context, []byte) (BatchCheckpointDescription, error) {
		return checkpointTestDescription(), nil
	}}, MaxMetadataBytes: 4096}})
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return store, owner, OriginNodeConfig{Enabled: true, Root: root, ForbiddenRoots: []string{t.TempDir()}}
}
func openNodeFixture(t *testing.T, s *BatchStore, c OriginNodeConfig) *OriginNodeData {
	t.Helper()
	n, e := NewOriginNodeData(s, c)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}
func TestOriginNodeGenesisReopenAndRetirement(t *testing.T) {
	s, owner, c := nodeFixture(t)
	n := openNodeFixture(t, s, c)
	if other, e := NewOriginNodeData(s, c); e == nil {
		_ = other.Close()
		t.Fatal("second local owner acquired namespace")
	}
	if e := s.InitializeNode(t.Context(), owner, []byte("0"), n); e != nil {
		t.Fatal(e)
	}
	first, e := s.ReadNodeOriginData(t.Context(), owner, n)
	if e != nil || first.LineageID == "" || len(first.Burns) != 0 || first.CreatorGeneration != "1" {
		t.Fatal(first, e)
	}
	if e = s.InitializeNode(t.Context(), owner, []byte("0"), n); e == nil {
		t.Fatal("reinitialization accepted")
	}
	// A journal-only burned orphan remains excluded data across ordinary reopen;
	// it does not recreate any SQL source or live authority.
	j, e := OpenOriginBurnJournal(t.Context(), true, filepath.Join(c.Root, first.ScopeHash), first.LineageID, first.JournalID)
	if e != nil {
		t.Fatal(e)
	}
	burn := OriginBurnData{Origin: "7", SourceID: "orphan-source", BindingDigest: "sha256:" + strings.Repeat("a", 64)}
	if _, e = j.AppendBurnData(t.Context(), burn); e != nil {
		t.Fatal(e)
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	if e = n.Close(); e != nil {
		t.Fatal(e)
	}
	n = openNodeFixture(t, s, c)
	reopened, e := s.ReadNodeOriginData(t.Context(), owner, n)
	if e != nil || len(reopened.Burns) != 1 || reopened.Burns[0] != burn || reopened.InitialBasis != first.InitialBasis {
		t.Fatal(reopened, e)
	}
	if batchCount(t, s, "mooring_ingress_sources") != 0 {
		t.Fatal("cold journal minted source")
	}
	// Creator provenance remains original even after an actual owner takeover.
	if e = s.config.Owners.Release(t.Context(), owner); e != nil {
		t.Fatal(e)
	}
	next, e := s.config.Owners.Acquire(t.Context(), owner.Key, "next-runtime")
	if e != nil {
		t.Fatal(e)
	}
	reopened, e = s.ReadNodeOriginData(t.Context(), next, n)
	if e != nil || reopened.CreatorGeneration != "1" || next.Generation == owner.Generation {
		t.Fatal(reopened, e)
	}
	if _, e = s.ReadNodeOriginData(t.Context(), owner, n); e == nil {
		t.Fatal("old owner revived")
	}
	if e = n.RetireBeforeRestore(t.Context()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.ReadNodeOriginData(t.Context(), next, n); e == nil {
		t.Fatal("retired node read")
	}
	if e = n.Close(); e != nil {
		t.Fatal(e)
	}
	if fresh, e := NewOriginNodeData(s, c); e == nil {
		_ = fresh.Close()
		t.Fatal("sticky retirement reopened")
	}
}
func TestOriginNodeOriginalAuthorityRollbackLeavesUnadoptableOrphan(t *testing.T) {
	for _, phase := range []string{"initial", "final", "commit"} {
		t.Run(phase, func(t *testing.T) {
			s, owner, c := nodeFixture(t)
			n := openNodeFixture(t, s, c)
			denied := errors.New("original authority revoked")
			calls := 0
			s.config.Authority = func(_ context.Context, _ *sql.Tx, a BatchAccess) error {
				calls++
				if phase == "initial" || phase == "final" && a.final {
					return denied
				}
				return nil
			}
			if phase == "commit" {
				// Deferred FK fails only at COMMIT, after initialization callback succeeded.
				if _, e := s.db.Exec(`CREATE TABLE node_test_parent(id INTEGER PRIMARY KEY); CREATE TABLE node_test_child(id INTEGER REFERENCES node_test_parent(id) DEFERRABLE INITIALLY DEFERRED); CREATE TRIGGER node_test_fault AFTER INSERT ON mooring_collab_audit BEGIN INSERT INTO node_test_child VALUES(9); END`); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.InitializeNode(t.Context(), owner, []byte("0"), n); e == nil {
				t.Fatal("failed authority/commit accepted")
			}
			for _, table := range []string{"mooring_collab_documents", "mooring_collab_checkpoints", "mooring_origin_node_anchors"} {
				if batchCount(t, s, table) != 0 {
					t.Fatal("partial SQL genesis", table)
				}
			}
			_, e := os.Stat(filepath.Join(c.Root, originNodeScopeHash(owner.Key)))
			if phase == "initial" {
				if !os.IsNotExist(e) || calls != 1 {
					t.Fatal("filesystem touched before authority", e, calls)
				}
				return
			}
			if e != nil {
				t.Fatal("burn namespace orphan disappeared", e)
			}
			s.config.Authority = func(context.Context, *sql.Tx, BatchAccess) error { return nil }
			if _, e = s.db.Exec(`DROP TRIGGER IF EXISTS node_test_fault`); e != nil {
				t.Fatal(e)
			}
			if e = s.InitializeNode(t.Context(), owner, []byte("0"), n); e == nil {
				t.Fatal("orphan namespace silently adopted")
			}
		})
	}
}
func TestOriginNodeLegacyAndCanonicalAnchorGuards(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		s, o, c := nodeFixture(t)
		n := openNodeFixture(t, s, c)
		if e := s.Initialize(t.Context(), o, []byte("0")); e != nil {
			t.Fatal(e)
		}
		if e := s.InitializeNode(t.Context(), o, []byte("0"), n); e == nil {
			t.Fatal("legacy upgraded")
		}
		if _, e := s.ReadNodeOriginData(t.Context(), o, n); e == nil {
			t.Fatal("legacy qualified")
		}
		if _, e := os.Stat(filepath.Join(c.Root, originNodeScopeHash(o.Key))); !os.IsNotExist(e) {
			t.Fatal("legacy touched filesystem", e)
		}
	})
	t.Run("retained", func(t *testing.T) {
		s, o, c := nodeFixture(t)
		n := openNodeFixture(t, s, c)
		if e := s.InitializeNode(t.Context(), o, []byte("0"), n); e != nil {
			t.Fatal(e)
		}
		for _, q := range []string{`DELETE FROM mooring_origin_node_anchors`, `UPDATE mooring_origin_node_anchors SET lineage_id='other'`, `UPDATE mooring_origin_node_anchors SET creator_generation='2'`, `UPDATE mooring_origin_node_anchors SET original_owner_token_hash=zeroblob(32)`, `INSERT OR REPLACE INTO mooring_origin_node_anchors SELECT * FROM mooring_origin_node_anchors`} {
			if _, e := s.db.Exec(q); e == nil {
				t.Fatal("anchor mutation accepted", q)
			}
		}
		if _, e := s.db.Exec(`DELETE FROM mooring_collab_documents WHERE page_id='p'`); e != nil {
			t.Fatal(e)
		}
		var closed int
		if e := s.db.QueryRow(`SELECT closed FROM mooring_origin_node_anchors`).Scan(&closed); e != nil || closed != 1 {
			t.Fatal(closed, e)
		}
		if _, e := s.db.Exec(`UPDATE mooring_origin_node_anchors SET closed=0`); e == nil {
			t.Fatal("retired anchor reopened")
		}
		if _, e := s.ReadNodeOriginData(t.Context(), o, n); e == nil {
			t.Fatal("deleted content qualified")
		}
	})
}
func TestOriginNodeRootQualificationAndPermanentPoison(t *testing.T) {
	s, _, c := nodeFixture(t)
	for _, mode := range []os.FileMode{0750, 0770, 0777} {
		if e := os.Chmod(c.Root, mode); e != nil {
			t.Fatal(e)
		}
		if n, e := NewOriginNodeData(s, c); e == nil {
			_ = n.Close()
			t.Fatal("nonprivate root accepted", mode)
		}
	}
	if e := os.Chmod(c.Root, 0700); e != nil {
		t.Fatal(e)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if e := os.Symlink(c.Root, alias); e != nil {
		t.Fatal(e)
	}
	bad := c
	bad.Root = alias
	if n, e := NewOriginNodeData(s, bad); e == nil {
		_ = n.Close()
		t.Fatal("symlink root accepted")
	}
	bad = c
	bad.ForbiddenRoots = []string{c.Root}
	if n, e := NewOriginNodeData(s, bad); e == nil {
		_ = n.Close()
		t.Fatal("destructive overlap accepted")
	}
	n := openNodeFixture(t, s, c)
	marker := filepath.Join(c.Root, originNodeRetiredName)
	if e := os.WriteFile(marker, []byte("refuse"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := n.acquire(t.Context(), s); e == nil {
		t.Fatal("retired marker ignored")
	}
	if e := os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	if _, e := n.acquire(t.Context(), s); e == nil {
		t.Fatal("marker removal revived object")
	}
}
