package mooring

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

var ErrConflict = errors.New("precondition_failed")
var ErrInvalid = errors.New("invalid Mooring command")

const MaxBytes = 32768
const MaxRevisions = 1000

type State struct {
	Rev        int64           `json:"rev"`
	Generation int64           `json:"generation"`
	Snapshot   json.RawMessage `json:"snapshot"`
}
type Request struct {
	// ReadSet is interpreted by the application adapter. Generic commands do not accept it.
	ReadSet    json.RawMessage `json:"read_set,omitempty"`
	MutationID string          `json:"mutation_id"`
	BaseRev    int64           `json:"base_rev"`
	Generation int64           `json:"generation"`
	Command    string          `json:"command"`
	Arguments  json.RawMessage `json:"arguments"`
}
type Receipt struct {
	MutationID  string `json:"mutation_id"`
	Rev         int64  `json:"rev"`
	Outcome     string `json:"outcome"`
	IssuedBy    string `json:"issued_by"`
	Durability  string `json:"durability"`
	Visibility  string `json:"visibility"`
	PayloadHash string `json:"payload_hash"`
	At          string `json:"at"`
}

// JSONStorage implements bounded, transaction-local canonical JSON persistence.
type JSONStorage struct{}

func (JSONStorage) Read(ctx context.Context, tx *sql.Tx, page string) (s State, err error) {
	var raw string
	err = tx.QueryRowContext(ctx, `SELECT rev,generation,snapshot FROM mooring_documents WHERE page_id=?`, page).Scan(&s.Rev, &s.Generation, &raw)
	s.Snapshot = json.RawMessage(raw)
	return
}
func Fingerprint(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func (JSONStorage) Commit(ctx context.Context, tx *sql.Tx, page, actor string, m Manifest, r Request) (Receipt, error) {
	if len(r.ReadSet) != 0 || len(r.MutationID) == 0 || len(r.MutationID) > 128 || r.BaseRev < 1 || r.Generation < 1 || len(r.Arguments) > MaxBytes {
		return Receipt{}, ErrInvalid
	}
	raw, _ := json.Marshal(r)
	fingerprint := Fingerprint(raw)
	var old, packet string
	err := tx.QueryRowContext(ctx, `SELECT fingerprint,receipt FROM mooring_receipts WHERE page_id=? AND authority_id=? AND mutation_id=?`, page, actor, r.MutationID).Scan(&old, &packet)
	if err == nil {
		if old != fingerprint {
			return Receipt{}, ErrConflict
		}
		var receipt Receipt
		err = json.Unmarshal([]byte(packet), &receipt)
		return receipt, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Receipt{}, err
	}
	state, err := (JSONStorage{}).Read(ctx, tx, page)
	if err != nil {
		return Receipt{}, err
	}
	if state.Rev != r.BaseRev || state.Generation != r.Generation || state.Rev >= MaxRevisions {
		return Receipt{}, ErrConflict
	}
	snapshot, err := m.Apply(r.Command, r.Arguments)
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	receipt := Receipt{MutationID: r.MutationID, Rev: state.Rev + 1, Outcome: "committed", IssuedBy: "crewship", Durability: "node", Visibility: "committed-revision", PayloadHash: Fingerprint(snapshot), At: tsformat.Format(time.Now())}
	data, _ := json.Marshal(receipt)
	var changed sql.Result
	if changed, err = tx.ExecContext(ctx, `UPDATE mooring_documents SET rev=?,snapshot=? WHERE page_id=? AND rev=? AND generation=?`, receipt.Rev, string(snapshot), page, r.BaseRev, r.Generation); err != nil {
		return Receipt{}, err
	}
	if n, e := changed.RowsAffected(); e != nil || n != 1 {
		return Receipt{}, ErrConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO mooring_receipts(page_id,authority_id,mutation_id,fingerprint,receipt,snapshot) VALUES(?,?,?,?,?,?)`, page, actor, r.MutationID, fingerprint, string(data), string(snapshot))
	return receipt, err
}

// VerifyCommitted runs after the final audit append and authorization recheck.
// SQLite triggers cannot erase or alter the state/receipt we are acknowledging.
func VerifyCommitted(ctx context.Context, tx *sql.Tx, page, actor string, m Manifest, r Request, receipt Receipt, before State) error {
	raw, _ := json.Marshal(r)
	var fingerprint, packet, snapshot string
	if e := tx.QueryRowContext(ctx, `SELECT fingerprint,receipt,snapshot FROM mooring_receipts WHERE page_id=? AND authority_id=? AND mutation_id=?`, page, actor, r.MutationID).Scan(&fingerprint, &packet, &snapshot); e != nil {
		return ErrConflict
	}
	expected, _ := json.Marshal(receipt)
	if fingerprint != Fingerprint(raw) || packet != string(expected) || receipt.PayloadHash != Fingerprint([]byte(snapshot)) {
		return ErrConflict
	}
	state, e := (JSONStorage{}).Read(ctx, tx, page)
	expectedRev := before.Rev
	expectedSnapshot := before.Snapshot
	if receipt.Rev > before.Rev {
		expectedRev = receipt.Rev
		expectedSnapshot = json.RawMessage(snapshot)
	}
	if e != nil || state.Generation != r.Generation || state.Rev != expectedRev || string(state.Snapshot) != string(expectedSnapshot) {
		return ErrConflict
	}
	if state.Rev == receipt.Rev && (string(state.Snapshot) != snapshot) {
		return ErrConflict
	}
	return nil
}
