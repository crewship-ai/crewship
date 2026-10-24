package mooring

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

var ErrOriginNodeData = errors.New("node origin data unavailable or unqualified")

const originNodeLockName = ".origin-node-owner.v1"
const originNodeRetiredName = ".origin-node-retired.v1"

// OriginNodeConfig is trusted application configuration, never request JSON.
// The host must supply ALL resolved destructive/content/log/mount roots. The
// actual DB directory is also discovered independently and always excluded.
// Deployment/reset/restore path qualification is a separate enablement gate.
type OriginNodeConfig struct {
	Enabled        bool
	Root           string
	ForbiddenRoots []string
}

// OriginNodeData owns a persistent local DATA namespace and lifetime lock.
// It neither issues sources nor authenticates physical/offhost recovery. Whole
// SQL+file prefix rollback is outside the node-restart contract, not detectable
// by matching hashes. No persisted row can reconstruct an author/source cap.
type OriginNodeData struct {
	mu       sync.RWMutex
	self     *OriginNodeData
	store    *BatchStore
	root     *os.Root
	path     string
	identity os.FileInfo
	lock     *os.File
	closed   bool
	released bool
	invalid  atomic.Bool
}

func NewOriginNodeData(store *BatchStore, c OriginNodeConfig) (*OriginNodeData, error) {
	if !c.Enabled || store == nil || store.ready() != nil || !originBurnSupported || !filepath.IsAbs(c.Root) || len(c.ForbiddenRoots) < 1 || len(c.ForbiddenRoots) > 32 {
		return nil, ErrOriginNodeData
	}
	specified, err := os.Lstat(filepath.Clean(c.Root))
	if err != nil || !specified.IsDir() || specified.Mode()&os.ModeSymlink != 0 {
		return nil, ErrOriginNodeData
	}
	path, err := filepath.EvalSymlinks(filepath.Clean(c.Root))
	if err != nil {
		return nil, ErrOriginNodeData
	}
	var databasePath string
	if err = store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil || !filepath.IsAbs(databasePath) {
		return nil, ErrOriginNodeData
	}
	forbidden := append([]string{filepath.Dir(databasePath)}, c.ForbiddenRoots...)
	for _, candidate := range forbidden {
		if !filepath.IsAbs(candidate) {
			return nil, ErrOriginNodeData
		}
		actual, e := filepath.EvalSymlinks(filepath.Clean(candidate))
		if e != nil || originNodeOverlaps(path, actual) {
			return nil, ErrOriginNodeData
		}
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, ErrOriginNodeData
	}
	fail := func() (*OriginNodeData, error) { _ = root.Close(); return nil, ErrOriginNodeData }
	info, err := root.Stat(".")
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return fail()
	}
	if _, err = root.Lstat(originNodeRetiredName); !os.IsNotExist(err) {
		return fail()
	}
	lock, err := openOriginNodeLock(root)
	if err != nil {
		return fail()
	}
	closeFail := func() (*OriginNodeData, error) { _ = lock.Close(); return fail() }
	locked, err := lock.Stat()
	if err != nil || !originBurnSafeFile(locked) || locked.Size() != 0 || lockOriginBurnFile(lock) != nil || lock.Sync() != nil || originBurnSyncRoot(root) != nil {
		return closeFail()
	}
	node := &OriginNodeData{store: store, root: root, path: path, identity: info, lock: lock}
	node.self = node
	if node.checkRoot() != nil {
		return closeFail()
	}
	return node, nil
}
func originNodeOverlaps(a, b string) bool {
	rel, err := filepath.Rel(a, b)
	if err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return true
	}
	rel, err = filepath.Rel(b, a)
	return err == nil && (rel == "." || rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
func originNodeScopeHash(key SessionKey) string {
	raw, _ := json.Marshal(struct{ Workspace, Application, Page string }{key.WorkspaceID, key.ApplicationID, key.ArtifactID})
	sum := sha256.Sum256(append([]byte("mooring.origin-node-scope.v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}
func (n *OriginNodeData) checkRoot() (failure error) {
	defer func() {
		if failure != nil && n != nil {
			n.invalid.Store(true)
		}
	}()
	if n == nil || n.self != n || n.closed || n.invalid.Load() {
		return ErrOriginNodeData
	}
	info, err := os.Stat(n.path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 || !os.SameFile(info, n.identity) {
		return ErrOriginNodeData
	}
	locked, err := n.lock.Stat()
	if err != nil || !originBurnSafeFile(locked) || locked.Size() != 0 {
		return ErrOriginNodeData
	}
	named, err := n.root.Lstat(originNodeLockName)
	if err != nil || !originBurnSafeFile(named) || !os.SameFile(locked, named) {
		return ErrOriginNodeData
	}
	if _, err = n.root.Lstat(originNodeRetiredName); !os.IsNotExist(err) {
		return ErrOriginNodeData
	}
	return nil
}
func (n *OriginNodeData) acquire(ctx context.Context, store *BatchStore) (func(), error) {
	if ctx == nil || ctx.Err() != nil || n == nil || n.self != n || n.store != store || !n.mu.TryRLock() {
		return nil, ErrOriginNodeData
	}
	if n.checkRoot() != nil {
		n.mu.RUnlock()
		return nil, ErrOriginNodeData
	}
	return n.mu.RUnlock, nil
}
func (n *OriginNodeData) Close() error {
	if n == nil || n.self != n {
		return ErrOriginNodeData
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.released {
		return nil
	}
	n.closed = true
	n.released = true
	return errors.Join(n.lock.Close(), n.root.Close())
}

// RetireBeforeRestore drains this object's DATA operations and fsyncs a sticky
// local refusal. The manager MUST separately drain original ingress/runtime/
// source leases BEFORE database mutation. This alone is not a restore protocol.
// There is deliberately no resume/unretire method; SQL rollback cannot clear it.
func (n *OriginNodeData) RetireBeforeRestore(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || n == nil || n.self != n {
		return ErrOriginNodeData
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.checkRoot() != nil {
		return ErrOriginNodeData
	}
	n.closed = true
	file, err := n.root.OpenFile(originNodeRetiredName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return ErrOriginNodeData
	}
	defer file.Close()
	const marker = "mooring.origin-node-retired.v1\n"
	if size, e := file.WriteString(marker); e != nil || size != len(marker) || file.Sync() != nil || originBurnSyncRoot(n.root) != nil || ctx.Err() != nil {
		return ErrOriginNodeData
	}
	return nil
}

type originNodeGenesis struct {
	scopeHash, lineage, journalID string
	journal                       *OriginBurnJournal
}

func (n *OriginNodeData) prepareGenesis(ctx context.Context, tx *sql.Tx, owner OwnerLease) (*originNodeGenesis, error) {
	if n.checkRoot() != nil || owner.Key.ApplicationID != "wordcraft" {
		return nil, ErrOriginNodeData
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM mooring_collab_documents WHERE page_id=?) OR EXISTS(SELECT 1 FROM mooring_origin_node_anchors WHERE page_id=?)`, owner.Key.ArtifactID, owner.Key.ArtifactID).Scan(&exists); err != nil || exists {
		return nil, ErrOriginNodeData
	}
	hash := originNodeScopeHash(owner.Key)
	if err := n.root.Mkdir(hash, 0o700); err != nil {
		return nil, ErrOriginNodeData
	}
	if originBurnSyncRoot(n.root) != nil {
		return nil, ErrOriginNodeData
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, ErrOriginNodeData
	}
	lineage := "lineage-" + hex.EncodeToString(nonce[:])
	journal, err := CreateFreshOriginBurnJournal(ctx, true, filepath.Join(n.path, hash), lineage)
	if err != nil {
		return nil, ErrOriginNodeData
	}
	actual, id, err := journal.IdentityData(ctx)
	if err != nil || actual != lineage {
		_ = journal.Close()
		return nil, ErrOriginNodeData
	}
	return &originNodeGenesis{hash, lineage, id, journal}, nil
}
func (n *OriginNodeData) persistGenesis(ctx context.Context, tx *sql.Tx, owner OwnerLease, g *originNodeGenesis) error {
	checkpoint, _, err := n.store.loadRetainedFullCheckpoint(ctx, tx, owner.Key, "0", true)
	if err != nil {
		return err
	}
	if n.checkRoot() != nil || checkpoint.Versions.ProtocolVersion != "0.4.0" || checkpoint.Versions.ModelVersion != "0.2.0" || checkpoint.Versions.EngineVersion != "0.10.0" {
		return ErrOriginNodeData
	}
	lineage, id, err := g.journal.IdentityData(ctx)
	if err != nil || lineage != g.lineage || id != g.journalID {
		return ErrOriginNodeData
	}
	entries, err := g.journal.EntriesData(ctx)
	if err != nil || len(entries) != 0 {
		return ErrOriginNodeData
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mooring_origin_node_anchors(page_id,workspace_id,application_id,lineage_id,scope_hash,journal_id,checkpoint_revision,checkpoint_digest,creator_generation,original_runtime_ref,original_owner_token_hash) VALUES(?,?,?,?,?,?,'0',?,?,?,?)`, owner.Key.ArtifactID, owner.Key.WorkspaceID, owner.Key.ApplicationID, g.lineage, g.scopeHash, g.journalID, string(checkpoint.Digest), strconv.FormatInt(owner.Generation, 10), owner.OwnerID, ownerTokenHash(owner.Token))
	return err
}

// OriginNodeSnapshotData is owned correlation/burn data, NOT a complete-inventory
// certificate, original-source proof, replay permission or live writer cap.
type OriginNodeSnapshotData struct {
	LineageID, JournalID, ScopeHash    string
	InitialBasis                       FullCheckpointBasis
	CreatorGeneration, OriginalRuntime string
	Burns                              []OriginBurnData
}

// ReadNodeOriginData uses the store's immutable original reader authority and
// current owner, in one FULL writer. Cold persisted rows never become a cap.
func (s *BatchStore) ReadNodeOriginData(ctx context.Context, owner OwnerLease, node *OriginNodeData) (OriginNodeSnapshotData, error) {
	var result OriginNodeSnapshotData
	release, err := node.acquire(ctx, s)
	if err != nil {
		return result, err
	}
	defer release()
	if s.ready() != nil || !s.checkpointIdentityEnabled() || !s.targetsEnabled() {
		return result, ErrOriginNodeData
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.authorize(ctx, tx, owner.Key, "read_checkpoint", nil, "", ""); err != nil {
			return err
		}
		if err := s.config.Owners.Check(ctx, tx, owner); err != nil {
			return err
		}
		var data OriginNodeSnapshotData
		var marker string
		var closed bool
		err := tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(a.lineage_id AS BLOB)) BETWEEN 1 AND 800 THEN a.lineage_id ELSE NULL END,CASE WHEN length(CAST(a.journal_id AS BLOB))=64 THEN a.journal_id ELSE NULL END,CASE WHEN length(CAST(a.scope_hash AS BLOB))=64 THEN a.scope_hash ELSE NULL END,CASE WHEN length(CAST(a.checkpoint_revision AS BLOB))=1 THEN a.checkpoint_revision ELSE NULL END,CASE WHEN length(CAST(a.checkpoint_digest AS BLOB))=71 THEN a.checkpoint_digest ELSE NULL END,CASE WHEN length(CAST(a.creator_generation AS BLOB)) BETWEEN 1 AND 20 THEN a.creator_generation ELSE NULL END,CASE WHEN length(CAST(a.original_runtime_ref AS BLOB)) BETWEEN 1 AND 800 THEN a.original_runtime_ref ELSE NULL END,a.closed,CASE WHEN length(CAST(d.origin_lineage_id AS BLOB)) BETWEEN 1 AND 800 THEN d.origin_lineage_id ELSE NULL END FROM mooring_origin_node_anchors a JOIN mooring_collab_documents d ON d.page_id=a.page_id AND d.workspace_id=a.workspace_id AND d.application_id=a.application_id WHERE a.page_id=? AND a.workspace_id=? AND a.application_id=?`, owner.Key.ArtifactID, owner.Key.WorkspaceID, owner.Key.ApplicationID).Scan(&data.LineageID, &data.JournalID, &data.ScopeHash, &data.InitialBasis.Revision, &data.InitialBasis.Digest, &data.CreatorGeneration, &data.OriginalRuntime, &closed, &marker)
		if err != nil {
			return err
		}
		if closed || marker != data.LineageID || data.ScopeHash != originNodeScopeHash(owner.Key) || !originBurnHex(data.JournalID, 64) {
			return ErrOriginNodeData
		}
		checkpoint, _, err := s.loadRetainedFullCheckpoint(ctx, tx, owner.Key, "0", false)
		if err != nil {
			return err
		}
		if data.InitialBasis.Revision != "0" || data.InitialBasis.Digest != string(checkpoint.Digest) || data.CreatorGeneration != string(checkpoint.Generation) {
			return ErrOriginNodeData
		}
		journal, err := OpenOriginBurnJournal(ctx, true, filepath.Join(node.path, data.ScopeHash), data.LineageID, data.JournalID)
		if err != nil {
			return err
		}
		defer journal.Close()
		data.Burns, err = journal.EntriesData(ctx)
		if err != nil {
			return err
		}
		if err = node.checkIssuedConsistency(ctx, tx, data); err != nil {
			return err
		}
		if node.checkRoot() != nil {
			return ErrOriginNodeData
		}
		if _, _, err = journal.IdentityData(ctx); err != nil {
			return err
		}
		if err = s.authorize(ctx, tx, owner.Key, "read_checkpoint", nil, "", ""); err != nil {
			return err
		}
		if err = s.config.Owners.Check(ctx, tx, owner); err != nil {
			return err
		}
		result = data
		return nil
	})
	if err != nil {
		return OriginNodeSnapshotData{}, err
	}
	return result, nil
}
func (n *OriginNodeData) checkIssuedConsistency(ctx context.Context, tx *sql.Tx, data OriginNodeSnapshotData) error {
	burns := make(map[string]OriginBurnData, len(data.Burns))
	for _, burn := range data.Burns {
		burns[burn.SourceID] = burn
	}
	for _, table := range []string{"mooring_origin_exclusions", "mooring_ingress_sources"} {
		query := `SELECT count(*) FROM ` + table + ` WHERE lineage_id=?`
		if table == "mooring_origin_exclusions" {
			query += ` AND kind='issued'`
		}
		var count int
		if err := tx.QueryRowContext(ctx, query, data.LineageID).Scan(&count); err != nil || count > MaxOriginInventoryEntries {
			return ErrOriginNodeData
		}
		columns := `CASE WHEN length(CAST(source_id AS BLOB)) BETWEEN 1 AND 800 THEN source_id ELSE NULL END,origin`
		if table == "mooring_ingress_sources" {
			columns += `,CASE WHEN length(CAST(author_binding_digest AS BLOB))=71 THEN author_binding_digest ELSE NULL END`
		}
		query = `SELECT ` + columns + ` FROM ` + table + ` WHERE lineage_id=?`
		if table == "mooring_origin_exclusions" {
			query += ` AND kind='issued'`
		}
		rows, err := tx.QueryContext(ctx, query, data.LineageID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var source, digest string
			var origin int64
			if table == "mooring_ingress_sources" {
				err = rows.Scan(&source, &origin, &digest)
			} else {
				err = rows.Scan(&source, &origin)
			}
			burn, ok := burns[source]
			if err != nil || !ok || burn.Origin != strconv.FormatInt(origin, 10) || digest != "" && burn.BindingDigest != digest {
				_ = rows.Close()
				return ErrOriginNodeData
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	}
	return nil
}
