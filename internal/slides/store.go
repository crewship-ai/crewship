package slides

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

var ErrInvalid = errors.New("invalid Slides mutation")

var ErrNotFound = errors.New("Slides not found")
var ErrDuplicate = errors.New("Slides mutation ID already used")
var ErrQuota = errors.New("Slides retained history quota exceeded")

type Key struct{ WorkspaceID, PageID string }

// Authorize must check the live Page principal/ACL through this transaction.
// Actor is returned by the trusted authority, never supplied in content.
type Authorize func(context.Context, *sql.Tx) (string, error)
type State struct {
	SchemaVersion int    `json:"schema_version"`
	Revision      int64  `json:"revision"`
	OrderVersion  int64  `json:"order_version"`
	ContentHash   string `json:"content_hash"`
	Deck          Deck   `json:"deck"`
}
type Receipt struct {
	MutationID   string `json:"mutation_id"`
	Revision     int64  `json:"revision"`
	OrderVersion int64  `json:"order_version"`
	ContentHash  string `json:"content_hash"`
	Duplicate    bool   `json:"duplicate"`
}
type Revision struct {
	Revision       int64  `json:"revision"`
	ParentRevision int64  `json:"parent_revision"`
	OrderVersion   int64  `json:"order_version"`
	SchemaVersion  int    `json:"schema_version"`
	ContentHash    string `json:"content_hash"`
	Actor          string `json:"actor"`
	Kind           string `json:"kind"`
	MutationID     string `json:"mutation_id"`
	CreatedAt      string `json:"created_at"`
}

// RevisionGuard is a trusted compiled integration hook. A future private asset
// adapter can validate and pin refs to this immutable revision in the SAME tx.
// It runs after final canonical validation and the immutable revision insert,
// before receipt/commit; any rejection rolls back all changes. No document-supplied code.
type RevisionGuard func(context.Context, *sql.Tx, Key, int64, Deck) error
type Options struct {
	MaxDecks, MaxRevisions, MaxObjectIDs int
	MaxHistoryBytes                      int64
	OnRevision                           RevisionGuard
}
type Store struct {
	db      *sql.DB
	options Options
}

func New(db *sql.DB, options Options) *Store {
	if options.MaxDecks <= 0 {
		options.MaxDecks = 128
	}
	if options.MaxRevisions <= 0 {
		options.MaxRevisions = 1000
	}
	if options.MaxObjectIDs <= 0 {
		options.MaxObjectIDs = 10000
	}
	if options.MaxHistoryBytes <= 0 {
		options.MaxHistoryBytes = 64 << 20
	}
	return &Store{db, options}
}
func (s *Store) tx(ctx context.Context, key Key, authorize Authorize, operation func(*sql.Tx, string) error) error {
	if key.WorkspaceID == "" || key.PageID == "" || authorize == nil {
		return errors.New("Slides key and live authority required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Establish SQLite's writer fence before even reads/replays, serializing live
	// ACL changes with check/use. This is not an author-controlled version field.
	if _, err = tx.ExecContext(ctx, `UPDATE slides_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return err
	}
	actor, err := authorize(ctx, tx)
	if err != nil {
		return err
	}
	if actor == "" {
		return errors.New("authorized actor required")
	}
	if err = operation(tx, actor); err != nil {
		return err
	}
	if confirmed, err := authorize(ctx, tx); err != nil {
		return err
	} else if confirmed != actor {
		return errors.New("Slides actor changed before commit")
	}
	return tx.Commit()
}
func readState(ctx context.Context, tx *sql.Tx, key Key, revision int64) (State, error) {
	var state State
	var data []byte
	var err error
	if revision == 0 {
		err = tx.QueryRowContext(ctx, `SELECT schema_version,head_rev,order_version,content_hash,canonical_json FROM slides_heads WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&state.SchemaVersion, &state.Revision, &state.OrderVersion, &state.ContentHash, &data)
	} else {
		err = tx.QueryRowContext(ctx, `SELECT schema_version,rev,order_version,content_hash,canonical_json FROM slides_revisions WHERE workspace_id=? AND page_id=? AND rev=?`, key.WorkspaceID, key.PageID, revision).Scan(&state.SchemaVersion, &state.Revision, &state.OrderVersion, &state.ContentHash, &data)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return state, err
	}
	if state.SchemaVersion != SchemaVersion {
		return state, ErrSchema
	}
	state.Deck, err = Decode(data)
	if err != nil {
		return state, err
	}
	hash, err := DeckHash(state.Deck)
	if err != nil {
		return state, err
	}
	if state.Deck.DeckID != key.PageID || hash != state.ContentHash {
		return state, errors.New("Slides stored identity/hash mismatch")
	}
	return state, nil
}
func (s *Store) Read(ctx context.Context, key Key, revision int64, authorize Authorize) (state State, err error) {
	if revision < 0 {
		return state, ErrNotFound
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error { var e error; state, e = readState(ctx, tx, key, revision); return e })
	return
}
func requestHash(key Key, kind string, request any) (string, error) {
	data, err := canonicalValue(request)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(append([]byte("crewship-slides-request/v1\n"+key.WorkspaceID+"\n"+key.PageID+"\n"+kind+"\n"), data...))
	return hex.EncodeToString(hash[:]), nil
}
func receipt(ctx context.Context, tx *sql.Tx, key Key, id, hash, actor, kind string) (Receipt, bool, error) {
	var result Receipt
	var recordedHash, recordedActor, recordedKind string
	err := tx.QueryRowContext(ctx, `SELECT mutation_id,rev,order_version,content_hash,request_hash,actor,kind FROM slides_receipts WHERE workspace_id=? AND page_id=? AND mutation_id=?`, key.WorkspaceID, key.PageID, id).Scan(&result.MutationID, &result.Revision, &result.OrderVersion, &result.ContentHash, &recordedHash, &recordedActor, &recordedKind)
	if errors.Is(err, sql.ErrNoRows) {
		// Forks retain immutable audit/revision mutation IDs but deliberately clear
		// replay receipts. A historical ID conflicts explicitly, never SQL500.
		var exists bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM slides_revisions WHERE workspace_id=? AND page_id=? AND mutation_id=?)`, key.WorkspaceID, key.PageID, id).Scan(&exists); err != nil {
			return result, false, err
		}
		if exists {
			return result, false, ErrDuplicate
		}
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if recordedHash != hash || recordedActor != actor || recordedKind != kind {
		return result, false, ErrDuplicate
	}
	result.Duplicate = true
	return result, true, nil
}
func (s *Store) quota(ctx context.Context, tx *sql.Tx, key Key, size int) error {
	var count int
	var retained int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(length(canonical_json)),0) FROM slides_revisions WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count, &retained); err != nil {
		return err
	}
	if count >= s.options.MaxRevisions || retained+int64(size) > s.options.MaxHistoryBytes {
		return ErrQuota
	}
	return nil
}
func reserveIDs(ctx context.Context, tx *sql.Tx, key Key, ids map[string]string, allowExisting bool, limit int) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slides_object_ids WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count); err != nil {
		return err
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		kind := ids[id]
		var recorded string
		err := tx.QueryRowContext(ctx, `SELECT object_kind FROM slides_object_ids WHERE workspace_id=? AND page_id=? AND object_id=?`, key.WorkspaceID, key.PageID, id).Scan(&recorded)
		if err == nil {
			if !allowExisting || recorded != kind {
				return ErrConflict
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		count++
		if count > limit {
			return ErrQuota
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO slides_object_ids(workspace_id,page_id,object_id,object_kind) VALUES(?,?,?,?)`, key.WorkspaceID, key.PageID, id, kind); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) write(ctx context.Context, tx *sql.Tx, key Key, current State, deck Deck, id, kind, hash, actor string, inserted map[string]string, restore bool) (Receipt, error) {
	var result Receipt
	if err := Validate(deck); err != nil {
		return result, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if deck.DeckID != key.PageID {
		return result, errors.New("Slides deck must retain Page identity")
	}
	canonical, err := Canonical(deck)
	if err != nil {
		return result, err
	}
	if len(canonical) > MaxDeckBytes {
		return result, ErrQuota
	}
	// Decode final bytes too: strict field/type rules cannot be bypassed through
	// an internal typed mutation path. The aggregate budget includes all elements.
	deck, err = Decode(canonical)
	if err != nil {
		return result, err
	}
	contentHash, err := DeckHash(deck)
	if err != nil {
		return result, err
	}
	if err = s.quota(ctx, tx, key, len(canonical)); err != nil {
		return result, err
	}
	revision, order := current.Revision+1, current.OrderVersion
	if !reflect.DeepEqual(current.Deck.SlideOrder, deck.SlideOrder) {
		order++
	}
	if order == 0 {
		order = 1
	}
	if current.Revision == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM slides_heads`).Scan(&count); err != nil {
			return result, err
		}
		if count >= s.options.MaxDecks {
			return result, ErrQuota
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES(?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, SchemaVersion, revision, order, canonical, contentHash); err != nil {
			return result, err
		}
	} else {
		updated, err := tx.ExecContext(ctx, `UPDATE slides_heads SET schema_version=?,head_rev=?,order_version=?,canonical_json=?,content_hash=? WHERE workspace_id=? AND page_id=? AND head_rev=?`, SchemaVersion, revision, order, canonical, contentHash, key.WorkspaceID, key.PageID, current.Revision)
		if err != nil {
			return result, err
		}
		affected, err := updated.RowsAffected()
		if err != nil {
			return result, err
		}
		if affected != 1 {
			return result, ErrConflict
		}
	}
	if err = reserveIDs(ctx, tx, key, inserted, restore, s.options.MaxObjectIDs); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO slides_revisions(workspace_id,page_id,rev,parent_rev,order_version,schema_version,canonical_json,content_hash,actor,kind,mutation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, revision, current.Revision, order, SchemaVersion, canonical, contentHash, actor, kind, id); err != nil {
		return result, err
	}
	if s.options.OnRevision != nil {
		if err = s.options.OnRevision(ctx, tx, key, revision, deck); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO slides_receipts(workspace_id,page_id,mutation_id,request_hash,actor,rev,order_version,kind,content_hash) VALUES(?,?,?,?,?,?,?,?,?)`, key.WorkspaceID, key.PageID, id, hash, actor, revision, order, kind, contentHash); err != nil {
		return result, err
	}
	return Receipt{MutationID: id, Revision: revision, OrderVersion: order, ContentHash: contentHash}, nil
}
func (s *Store) Initialize(ctx context.Context, key Key, request InitializeRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeInitialize(raw)
	if err != nil {
		return result, err
	}
	canonical, err := Canonical(request.Deck)
	if err != nil {
		return result, err
	}
	request.Deck, err = Decode(canonical)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "initialize", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "initialize")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		if _, err = readState(ctx, tx, key, 0); err == nil {
			return ErrConflict
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		result, err = s.write(ctx, tx, key, State{}, request.Deck, request.MutationID, "initialize", hash, actor, ObjectIDs(request.Deck), false)
		return err
	})
	return
}
func insertedIDs(request PatchRequest) map[string]string {
	ids := map[string]string{}
	for _, op := range request.Operations {
		if op.Op == "insert_slide" && op.Slide != nil {
			for id, kind := range ObjectIDs(Deck{Slides: []Slide{*op.Slide}}) {
				ids[id] = kind
			}
		}
		if op.Op == "insert_element" && op.Element != nil {
			ids[op.Element.ID] = "element"
		}
	}
	return ids
}
func (s *Store) Patch(ctx context.Context, key Key, request PatchRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodePatch(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "patch", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "patch")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.Revision != request.BaseRev || (request.IfOrderVersion != nil && *request.IfOrderVersion != current.OrderVersion) {
			return ErrConflict
		}
		candidate, err := ApplyPatch(current.Deck, request)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				return err
			}
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		result, err = s.write(ctx, tx, key, current, candidate, request.MutationID, "patch", hash, actor, insertedIDs(request), false)
		return err
	})
	return
}
func (s *Store) Reorder(ctx context.Context, key Key, request ReorderRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeReorder(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "reorder", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "reorder")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.Revision != request.BaseRev || current.OrderVersion != request.IfOrderVersion {
			return ErrConflict
		}
		candidate := current.Deck
		candidate.SlideOrder = append([]string{}, request.SlideOrder...)
		result, err = s.write(ctx, tx, key, current, candidate, request.MutationID, "reorder", hash, actor, nil, false)
		return err
	})
	return
}
func (s *Store) Restore(ctx context.Context, key Key, request RestoreRequest, authorize Authorize) (result Receipt, err error) {
	raw, err := json.Marshal(request)
	if err != nil {
		return result, err
	}
	request, err = DecodeRestore(raw)
	if err != nil {
		return result, err
	}
	hash, err := requestHash(key, "restore", request)
	if err != nil {
		return result, err
	}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, actor string) error {
		if request.PageID != key.PageID {
			return ErrConflict
		}
		previous, found, err := receipt(ctx, tx, key, request.MutationID, hash, actor, "restore")
		if err != nil {
			return err
		}
		if found {
			result = previous
			return nil
		}
		current, err := readState(ctx, tx, key, 0)
		if err != nil {
			return err
		}
		if current.Revision != request.BaseRev || current.OrderVersion != request.IfOrderVersion {
			return ErrConflict
		}
		target, err := readState(ctx, tx, key, request.Revision)
		if err != nil {
			return err
		}
		result, err = s.write(ctx, tx, key, current, target.Deck, request.MutationID, "restore", hash, actor, ObjectIDs(target.Deck), true)
		return err
	})
	return
}
func (s *Store) History(ctx context.Context, key Key, authorize Authorize) (history []Revision, err error) {
	history = []Revision{}
	err = s.tx(ctx, key, authorize, func(tx *sql.Tx, _ string) error {
		if _, err := readState(ctx, tx, key, 0); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT rev,parent_rev,order_version,schema_version,content_hash,actor,kind,mutation_id,created_at FROM slides_revisions WHERE workspace_id=? AND page_id=? ORDER BY rev DESC LIMIT ?`, key.WorkspaceID, key.PageID, s.options.MaxRevisions)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var revision Revision
			if err = rows.Scan(&revision.Revision, &revision.ParentRevision, &revision.OrderVersion, &revision.SchemaVersion, &revision.ContentHash, &revision.Actor, &revision.Kind, &revision.MutationID, &revision.CreatedAt); err != nil {
				return err
			}
			history = append(history, revision)
		}
		return rows.Err()
	})
	return
}
