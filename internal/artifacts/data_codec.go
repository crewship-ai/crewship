package artifacts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
)

const MaxDataResponseBytes = 16 << 20

// The immutable tool/RPC metadata has a fixed allowance outside the batch.
// DecodeDataBatch still enforces the original 1 MiB document batch bound.
const MaxDataToolRequestBytes = MaxDataBatchBytes + 8192

// MarshalDataJSON preserves the stored JSON byte budget across JSON-only
// transports: HTML escaping would expand a valid '<' value up to sixfold.
func MarshalDataJSON(value any) ([]byte, error) {
	// Go 1.27's v1-compatible Encoder keeps EscapeForJS enabled even after
	// SetEscapeHTML(false). Explicit JSON-only options preserve literal LS/PS
	// as well as HTML-like values, while retaining legacy wire conventions.
	return jsonv2.Marshal(value, json.DefaultOptionsV1(), jsontext.EscapeForHTML(false), jsontext.EscapeForJS(false))
}

type DataBatch struct {
	BaseVersion int64       `json:"base_version"`
	Documents   []DataWrite `json:"documents"`
}

// DecodeDataJSON checks duplicate keys and bounded nesting without restricting
// user document keys or scalar values. Transport structs remain closed.
func DecodeDataJSON(raw []byte, out any, fields ...string) error {
	if len(raw) > MaxDataToolRequestBytes || !validDataObject(raw) {
		return ErrDataInvalid
	}
	if !canonicalDataFields(raw, fields...) {
		return ErrDataInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return err
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return ErrDataInvalid
	}
	return nil
}

func canonicalDataFields(raw []byte, fields ...string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	for key := range object {
		found := false
		for _, field := range fields {
			if key == field {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func (w *DataWrite) UnmarshalJSON(raw []byte) error {
	if !validDataObject(raw) || !canonicalDataFields(raw, "id", "ifVersion", "value") {
		return ErrDataInvalid
	}
	var wire struct {
		ID        *string         `json:"id"`
		IfVersion *int64          `json:"ifVersion"`
		Value     json.RawMessage `json:"value"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&wire) != nil || wire.ID == nil || wire.IfVersion == nil || !dataIdentifier.MatchString(*wire.ID) || *wire.IfVersion < 0 || *wire.IfVersion >= MaxDataVersion || !validDataObject(wire.Value) {
		return ErrDataInvalid
	}
	if len(wire.Value) > MaxDataDocumentBytes {
		return ErrDataQuota
	}
	*w = DataWrite{ID: *wire.ID, IfVersion: *wire.IfVersion, Value: wire.Value}
	return nil
}

// DecodeDataBatch is shared by the HTTP and CLI codecs. Counters never pass
// through float64; nested object duplicates and unknown authority fields fail.
func DecodeDataBatch(raw []byte) (DataBatch, error) {
	var body DataBatch
	if len(raw) > MaxDataBatchBytes {
		return body, ErrDataQuota
	}
	if !validDataObject(raw) || !canonicalDataFields(raw, "base_version", "documents") {
		return body, ErrDataInvalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		if errors.Is(err, ErrDataQuota) {
			return DataBatch{}, ErrDataQuota
		}
		return DataBatch{}, ErrDataInvalid
	}
	if body.BaseVersion < 1 || body.BaseVersion > MaxDataVersion || len(body.Documents) < 1 || len(body.Documents) > MaxDataBatchWrites {
		return DataBatch{}, ErrDataInvalid
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		return DataBatch{}, ErrDataInvalid
	}
	return body, nil
}

// DashboardCollectionsTx reads only the bounded declaration from one immutable
// code version. Call inside DataStore's authority transaction, never as a stale
// preflight replacing the current ACL, selected shared pin or final recheck.
func DashboardCollectionsTx(ctx context.Context, tx *sql.Tx, key VersionKey, version int64) ([]string, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT b.content FROM artifact_versions v JOIN artifact_version_blobs b ON b.workspace_id=v.workspace_id AND b.page_id=v.page_id AND b.hash=json_extract(v.manifest_json,'$.files."dashboard.json".hash') WHERE v.workspace_id=? AND v.page_id=? AND v.version=? AND length(b.content)<=16384`, key.WorkspaceID, key.PageID, version).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrVersionNotFound
	}
	if err != nil {
		return nil, err
	}
	return ParseDashboardCollections(raw)
}

// ParseDashboardCollections validates the bounded code declaration shared by
// authorized data reads and Page kind metadata.
func ParseDashboardCollections(raw []byte) ([]string, error) {
	if len(raw) > 16384 || !validDataObject(raw) {
		return nil, ErrVersionInvalid
	}
	var declaration struct {
		SchemaVersion int      `json:"schema_version"`
		Collections   []string `json:"collections"`
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if !canonicalDataFields(raw, "schema_version", "collections") || d.Decode(&declaration) != nil || declaration.SchemaVersion != 1 || len(declaration.Collections) < 1 || len(declaration.Collections) > MaxDataCollections {
		return nil, ErrVersionInvalid
	}
	seen := map[string]bool{}
	for _, name := range declaration.Collections {
		if !dataIdentifier.MatchString(name) || seen[name] {
			return nil, ErrVersionInvalid
		}
		seen[name] = true
	}
	return declaration.Collections, nil
}
