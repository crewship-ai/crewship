package journal

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
)

var ErrCheckpointConflict = errors.New("audit checkpoint tail changed")

// ResealCheckpoint is an explicit OWNER attestation of a new segment, not a
// claim that missing historical keys authenticated the old prefix. Its second,
// purpose-separated MAC prevents generic event emitters from creating anchors.
type ResealCheckpoint struct {
	ID               string `json:"id"`
	Scope            string `json:"scope"`
	WorkspaceID      string `json:"workspace_id,omitempty"`
	ActorID          string `json:"actor_id"`
	CreatedAt        string `json:"created_at"`
	OldTailSeq       int64  `json:"old_tail_seq"`
	OldTailHash      string `json:"old_tail_hash"`
	Reason           string `json:"reason"`
	HistoricalStatus string `json:"historical_status"`
	KeyID            string `json:"key_id"`
	Version          int    `json:"version"`
	MAC              string `json:"checkpoint_mac"`
}

type CheckpointRow struct {
	ID, Scope, WorkspaceID, ActorID, CreatedAt, PrevHash, Payload string
	Seq                                                           int64
	KeyID                                                         sql.NullString
	Version                                                       sql.NullInt64
}

func checkpointMAC(keys *SigningKeys, checkpoint ResealCheckpoint) (string, error) {
	key, err := keys.verification(sql.NullString{String: checkpoint.KeyID, Valid: true}, sql.NullInt64{Int64: 2, Valid: true})
	if err != nil {
		return "", err
	}
	checkpoint.MAC = ""
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	return versionedMAC(key, "crewship explicit OWNER audit checkpoint", checkpoint.KeyID, string(data)), nil
}
func SealResealCheckpoint(keys *SigningKeys, checkpoint ResealCheckpoint) (ResealCheckpoint, error) {
	if keys == nil || keys.ActiveID() == "" {
		return checkpoint, ErrSigningKeyUnavailable
	}
	checkpoint.KeyID, checkpoint.Version = keys.ActiveID(), 2
	checkpoint.Reason = strings.TrimSpace(checkpoint.Reason)
	if checkpoint.ID == "" || checkpoint.ActorID == "" || checkpoint.CreatedAt == "" || (checkpoint.OldTailSeq < 0 || checkpoint.OldTailSeq == math.MaxInt64) || len(checkpoint.Reason) < 8 || len(checkpoint.Reason) > 512 {
		return checkpoint, errors.New("audit checkpoint: invalid identity, tail or reason")
	}
	if (checkpoint.Scope != "instance" && checkpoint.Scope != "workspace") || (checkpoint.Scope == "workspace") != (checkpoint.WorkspaceID != "") {
		return checkpoint, errors.New("audit checkpoint: invalid scope")
	}
	if checkpoint.HistoricalStatus != "historical_unverified" && checkpoint.HistoricalStatus != "historical_invalid" && !(checkpoint.OldTailSeq == 0 && checkpoint.HistoricalStatus == "verified") {
		return checkpoint, errors.New("audit checkpoint: invalid historical status")
	}
	mac, err := checkpointMAC(keys, checkpoint)
	checkpoint.MAC = mac
	return checkpoint, err
}
func ResealPayload(checkpoint ResealCheckpoint) map[string]any {
	return map[string]any{"action": "audit.reseal", "checkpoint": checkpoint}
}
func ValidateResealRow(keys *SigningKeys, row CheckpointRow) (*ResealCheckpoint, bool) {
	var envelope struct {
		Action     string           `json:"action"`
		Checkpoint ResealCheckpoint `json:"checkpoint"`
	}
	if json.Unmarshal([]byte(row.Payload), &envelope) != nil || envelope.Action != "audit.reseal" {
		return nil, false
	}
	c := envelope.Checkpoint
	if c.Version != 2 || c.ID != row.ID || c.Scope != row.Scope || c.WorkspaceID != row.WorkspaceID || c.ActorID != row.ActorID || c.ActorID == "" || c.CreatedAt != row.CreatedAt || c.OldTailSeq < 0 || row.Seq != c.OldTailSeq+1 || row.PrevHash != c.OldTailHash || !row.KeyID.Valid || row.KeyID.String != c.KeyID || !row.Version.Valid || row.Version.Int64 != 2 {
		return nil, false
	}
	if c.HistoricalStatus != "historical_unverified" && c.HistoricalStatus != "historical_invalid" && !(c.OldTailSeq == 0 && c.HistoricalStatus == "verified") {
		return nil, false
	}
	mac, err := checkpointMAC(keys, c)
	if err != nil || !hmac.Equal([]byte(mac), []byte(c.MAC)) {
		return nil, false
	}
	return &c, true
}

// AppendResealTx is the one OWNER checkpoint consumer. Ordinary AppendTx never
// gains this internal admission flag, even for lookalike client event payloads.
// API authority and final native row/state ACK still belong to the caller TX.
func (w *Writer) AppendResealTx(ctx context.Context, tx *sql.Tx, e Entry) (func(), error) {
	if w.signingKeyError != nil {
		return nil, w.signingKeyError
	}
	e, err := prepareEntry(ctx, e)
	if err != nil {
		return nil, err
	}
	head, err := loadChainHead(ctx, tx, e.WorkspaceID)
	if err != nil {
		return nil, err
	}
	payload, err := e.payloadJSON()
	if err != nil {
		return nil, err
	}
	keyID, version := ActiveSigningIdentity(w.signingKeys)
	row := CheckpointRow{ID: e.ID, Scope: "workspace", WorkspaceID: e.WorkspaceID, ActorID: e.ActorID, CreatedAt: e.TS.UTC().Format("2006-01-02T15:04:05.000Z"), Seq: head.seq + 1, PrevHash: head.prevHash, Payload: payload, KeyID: keyID, Version: version}
	if _, valid := ValidateResealRow(w.signingKeys, row); !valid || e.ActorType != ActorUser || e.Type != EntryAuditEntityRestored {
		return nil, ErrCheckpointConflict
	}
	e.reseal = true
	if err := w.persistBatchTx(ctx, tx, []Entry{e}, true); err != nil {
		return nil, err
	}
	return func() { w.afterCommit([]Entry{e}) }, nil
}

func workspaceResealRow(f ChainFields, prev, payload string) CheckpointRow {
	return CheckpointRow{ID: f.ID, Scope: "workspace", WorkspaceID: f.Workspace, ActorID: f.ActorID, CreatedAt: f.TS, Seq: f.Seq, PrevHash: prev, Payload: payload, KeyID: f.KeyID, Version: f.MACVersion}
}

// Probe only checkpoint candidates, then authenticate both the ordinary row MAC
// and the OWNER-specific checkpoint MAC. A forged/ordinary payload is no anchor.
func latestWorkspaceCheckpoint(ctx context.Context, db chainReader, workspace string, keys *SigningKeys) (*ResealCheckpoint, error) {
	query, _, err := verifySelectFor(ctx, db)
	if err != nil {
		return nil, err
	}
	query = strings.Replace(query, "WHERE workspace_id = ?", "WHERE workspace_id = ? AND entry_type='audit.entity_restored' AND CASE WHEN json_valid(payload) THEN json_extract(payload,'$.action')='audit.reseal' ELSE 0 END", 1)
	query = strings.Replace(query, "ORDER BY seq ASC", "ORDER BY seq DESC", 1)
	rows, err := db.QueryContext(ctx, query, workspace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		f, prev, hash, _, err := scanChainRow(rows)
		if err != nil {
			return nil, err
		}
		mac, err := chainHashForKeys(keys, prev, f)
		if err != nil || !hmac.Equal([]byte(mac), []byte(hash)) || f.ActorType != string(ActorUser) {
			continue
		}
		if checkpoint, ok := ValidateResealRow(keys, workspaceResealRow(f, prev, f.Payload)); ok {
			return checkpoint, nil
		}
	}
	return nil, rows.Err()
}
