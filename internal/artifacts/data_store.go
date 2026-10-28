package artifacts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

const (
	MaxDataDocumentBytes         = 256 << 10
	MaxDataBatchWrites           = 50
	MaxDataBatchBytes            = 1 << 20
	MaxDataCollectionBytes       = 8 << 20
	MaxDataCollections           = 32
	MaxDataDocuments             = 4096
	MaxDataVersion         int64 = 9007199254740991
)

var (
	ErrDataInvalid  = errors.New("invalid artifact dataset request")
	ErrDataConflict = errors.New("artifact document version conflict")
	ErrDataQuota    = errors.New("artifact dataset quota exceeded")
)

type DataOptions struct{ Enabled bool }
type DataWrite struct {
	ID        string          `json:"id"`
	IfVersion int64           `json:"ifVersion"`
	Value     json.RawMessage `json:"value"`
}
type DataDocument struct {
	ID        string          `json:"id"`
	Version   int64           `json:"version"`
	Value     json.RawMessage `json:"value"`
	UpdatedBy string          `json:"updated_by"`
	UpdatedAt string          `json:"updated_at"`
}
type DataCollection struct {
	Name      string `json:"name"`
	Documents int    `json:"documents"`
	Bytes     int64  `json:"bytes"`
}
type DataStore struct {
	db   *sql.DB
	opts DataOptions
}

func NewDataStore(db *sql.DB, opts DataOptions) *DataStore { return &DataStore{db: db, opts: opts} }

var dataIdentifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// transaction reuses the existing VersionStore durability, writer fence and
// before/after authority checks. Data never changes a code manifest or its pins.
// A reader's callback must check its collection allowlist against the selected
// shared manifest in this transaction, alongside current Page access.
func (s *DataStore) transaction(ctx context.Context, key VersionKey, auth VersionAuthorize, fn func(*sql.Tx, string) error) error {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return ErrDisabled
	}
	return NewVersionStore(s.db, VersionOptions{Enabled: true}).transaction(ctx, key, auth, func(tx *sql.Tx, actor string) error {
		var found int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_version_heads WHERE workspace_id=? AND page_id=? AND content_type='artifact_project'`, key.WorkspaceID, key.PageID).Scan(&found); err != nil {
			return err
		}
		if found != 1 {
			return ErrVersionNotFound
		}
		if len(actor) > 256 || !utf8.ValidString(actor) {
			return ErrDataInvalid
		}
		return fn(tx, actor)
	})
}

// Batch creates a collection implicitly and atomically upserts documents. Zero
// ifVersion means absent; positive values must match the exact stored counter.
// Input has no attribution fields: the current host authority supplies them.
func (s *DataStore) Batch(ctx context.Context, key VersionKey, collection string, writes []DataWrite, auth VersionAuthorize) ([]DataDocument, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, ErrDisabled
	}
	if !dataIdentifier.MatchString(collection) || len(writes) == 0 || len(writes) > MaxDataBatchWrites {
		return nil, ErrDataInvalid
	}
	seen := map[string]bool{}
	// Batch receives writes, not the code base_version. Account the smallest
	// valid request envelope here; ingress codecs separately bound the actual
	// whole request. Encode metadata only so value whitespace, Unicode and
	// numeric lexemes retain their original byte cost.
	batchBytes := len(`{"base_version":1,"documents":[]}`)
	for i, w := range writes {
		if !dataIdentifier.MatchString(w.ID) || seen[w.ID] || w.IfVersion < 0 || w.IfVersion >= MaxDataVersion {
			return nil, ErrDataInvalid
		}
		seen[w.ID] = true
		if len(w.Value) > MaxDataDocumentBytes {
			return nil, ErrDataQuota
		}
		if !validDataObject(w.Value) {
			return nil, ErrDataInvalid
		}
		metadata, err := json.Marshal(DataWrite{ID: w.ID, IfVersion: w.IfVersion, Value: json.RawMessage(`{}`)})
		if err != nil {
			return nil, ErrDataInvalid
		}
		batchBytes += len(metadata) - len(`{}`) + len(w.Value)
		if i > 0 {
			batchBytes++ // comma between document objects
		}
		if batchBytes > MaxDataBatchBytes {
			return nil, ErrDataQuota
		}
	}
	var out []DataDocument
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, actor string) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artifact_data_collections WHERE workspace_id=? AND page_id=? AND name=?)`, key.WorkspaceID, key.PageID, collection).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			var count int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM artifact_data_collections WHERE workspace_id=? AND page_id=?`, key.WorkspaceID, key.PageID).Scan(&count); err != nil {
				return err
			}
			if count >= MaxDataCollections {
				return ErrDataQuota
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES(?,?,?)`, key.WorkspaceID, key.PageID, collection); err != nil {
				return err
			}
		}
		stamp := tsformat.Format(time.Now().UTC())
		for _, w := range writes {
			var version int64
			err := tx.QueryRowContext(ctx, `SELECT version FROM artifact_data_documents WHERE workspace_id=? AND page_id=? AND collection=? AND id=?`, key.WorkspaceID, key.PageID, collection, w.ID).Scan(&version)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if version != w.IfVersion {
				return ErrDataConflict
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(workspace_id,page_id,collection,id) DO UPDATE SET version=excluded.version,value_json=excluded.value_json,updated_by=excluded.updated_by,updated_at=excluded.updated_at`, key.WorkspaceID, key.PageID, collection, w.ID, version+1, string(w.Value), actor, stamp); err != nil {
				return err
			}
			out = append(out, DataDocument{ID: w.ID, Version: version + 1, Value: bytes.Clone(w.Value), UpdatedBy: actor, UpdatedAt: stamp})
		}
		var count int
		var size int64
		if err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(sum(length(CAST(value_json AS BLOB))),0) FROM artifact_data_documents WHERE workspace_id=? AND page_id=? AND collection=?`, key.WorkspaceID, key.PageID, collection).Scan(&count, &size); err != nil {
			return err
		}
		if count > MaxDataDocuments || size > MaxDataCollectionBytes {
			return ErrDataQuota
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *DataStore) Read(ctx context.Context, key VersionKey, collection string, auth VersionAuthorize) ([]DataDocument, error) {
	if s == nil || s.db == nil || !s.opts.Enabled {
		return nil, ErrDisabled
	}
	if !dataIdentifier.MatchString(collection) {
		return nil, ErrDataInvalid
	}
	out := []DataDocument{}
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, _ string) error {
		rows, err := tx.QueryContext(ctx, `SELECT id,version,value_json,updated_by,updated_at FROM artifact_data_documents WHERE workspace_id=? AND page_id=? AND collection=? ORDER BY id LIMIT ?`, key.WorkspaceID, key.PageID, collection, MaxDataDocuments+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		size := 0
		for rows.Next() {
			var doc DataDocument
			var raw string
			if err := rows.Scan(&doc.ID, &doc.Version, &raw, &doc.UpdatedBy, &doc.UpdatedAt); err != nil {
				return err
			}
			size += len(raw)
			if len(raw) > MaxDataDocumentBytes || size > MaxDataCollectionBytes || len(out) >= MaxDataDocuments {
				return ErrDataQuota
			}
			if doc.Version < 1 || doc.Version > MaxDataVersion || !validDataObject([]byte(raw)) {
				return ErrDataInvalid
			}
			doc.Value = json.RawMessage(raw)
			out = append(out, doc)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Collections is intended for an owner/agent enumerating its dataset names.
// A shared-reader API must use Read with an exact collection-scoped callback;
// enumeration itself is not a grant to inspect unshared collections.
func (s *DataStore) Collections(ctx context.Context, key VersionKey, auth VersionAuthorize) ([]DataCollection, error) {
	out := []DataCollection{}
	err := s.transaction(ctx, key, auth, func(tx *sql.Tx, _ string) error {
		rows, err := tx.QueryContext(ctx, `SELECT c.name,count(d.id),coalesce(sum(length(CAST(d.value_json AS BLOB))),0) FROM artifact_data_collections c LEFT JOIN artifact_data_documents d ON d.workspace_id=c.workspace_id AND d.page_id=c.page_id AND d.collection=c.name WHERE c.workspace_id=? AND c.page_id=? GROUP BY c.name ORDER BY c.name LIMIT ?`, key.WorkspaceID, key.PageID, MaxDataCollections+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c DataCollection
			if err := rows.Scan(&c.Name, &c.Documents, &c.Bytes); err != nil {
				return err
			}
			if len(out) >= MaxDataCollections {
				return ErrDataQuota
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Validate without decoding numbers into float64 or rewriting caller JSON.
// Duplicate decoded object keys and excessive nesting are rejected everywhere.
func validDataObject(raw []byte) bool {
	if !utf8.Valid(raw) || !json.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if dataJSONValue(d, 0) != nil {
		return false
	}
	_, err := d.Token()
	return errors.Is(err, io.EOF)
}
func dataJSONValue(d *json.Decoder, depth int) error {
	if depth > 64 {
		return ErrDataInvalid
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return ErrDataInvalid
			}
			seen[name] = true
			if err := dataJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := dataJSONValue(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: unexpected JSON delimiter", ErrDataInvalid)
	}
	_, err = d.Token()
	return err
}
