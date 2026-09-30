package backupplan

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"filippo.io/age"
)

// Recipient is one backup key (GET/POST …/backups/recipients): the public
// half of an age key pair backups are encrypted to. The private half never
// reaches the server.
type Recipient struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Holder    string `json:"holder"`
	// Fingerprint is a short, stable digest of the public key for a printed
	// recovery sheet: "SHA256:1a2b3c4d5e6f7a8b".
	Fingerprint string  `json:"fingerprint"`
	CreatedBy   *string `json:"created_by"`
	CreatedAt   string  `json:"created_at"`
	// UsedBy names the plans that encrypt to this key.
	UsedBy []string `json:"used_by"`
}

// ErrRecipientInUse: a plan still encrypts to the key.
var ErrRecipientInUse = errors.New("backupplan: the backup key is used by a plan")

// ErrDuplicateRecipient: the same public key is already a recipient.
var ErrDuplicateRecipient = errors.New("backupplan: that public key is already a backup key")

// KeyFingerprint is "SHA256:" and the first 16 hex digits of the key's
// SHA-256.
func KeyFingerprint(publicKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(publicKey)))
	return "SHA256:" + hex.EncodeToString(sum[:8])
}

// ListRecipients returns every backup key, oldest first, with the plans
// that use it.
func ListRecipients(ctx context.Context, db *sql.DB) ([]Recipient, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name, public_key, holder, created_by, created_at FROM backup_recipients ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	out := []Recipient{}
	for rows.Next() {
		var r Recipient
		var by sql.NullString
		if err := rows.Scan(&r.ID, &r.Name, &r.PublicKey, &r.Holder, &by, &r.CreatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		r.CreatedBy, r.Fingerprint, r.UsedBy = strPtr(by), KeyFingerprint(r.PublicKey), []string{}
		out = append(out, r)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	plans, err := ListPlans(ctx, db)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].UsedBy = plansUsing(plans, out[i].ID, out[i].PublicKey)
	}
	return out, nil
}

func plansUsing(plans []*Plan, id, key string) []string {
	used := []string{}
	for _, p := range plans {
		for _, rid := range p.RecipientIDs {
			if rid == id || rid == key {
				used = append(used, p.Name)
				break
			}
		}
	}
	return used
}

// GetRecipient returns one backup key or ErrNotFound.
func GetRecipient(ctx context.Context, db *sql.DB, id string) (*Recipient, error) {
	var r Recipient
	var by sql.NullString
	err := db.QueryRowContext(ctx, `SELECT id, name, public_key, holder, created_by, created_at FROM backup_recipients WHERE id = ?`, id).
		Scan(&r.ID, &r.Name, &r.PublicKey, &r.Holder, &by, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedBy, r.Fingerprint, r.UsedBy = strPtr(by), KeyFingerprint(r.PublicKey), []string{}
	return &r, nil
}

// NormalizeRecipient validates a new backup key: a name, and an age X25519
// public key (age1…). A private key pasted by mistake is refused with a
// message that says so.
func NormalizeRecipient(r *Recipient) error {
	r.Name, r.Holder, r.PublicKey = strings.TrimSpace(r.Name), strings.TrimSpace(r.Holder), strings.TrimSpace(r.PublicKey)
	if r.Name == "" || len(r.Name) > 120 {
		return invalid("name is required (at most 120 characters)")
	}
	if len(r.Holder) > 200 {
		return invalid("holder is at most 200 characters")
	}
	if strings.HasPrefix(strings.ToUpper(r.PublicKey), "AGE-SECRET-KEY-") {
		return invalid("that is a PRIVATE key: give the public key (age1…), and keep the private half off this server")
	}
	if _, err := age.ParseX25519Recipient(r.PublicKey); err != nil {
		return invalid("public_key is not a valid age public key (age1…): %v", err)
	}
	return nil
}

// InsertRecipient stores a normalized backup key; r.ID is set.
func InsertRecipient(ctx context.Context, ex execer, r *Recipient, actor string, now time.Time) error {
	r.ID = newID("brk_")
	r.CreatedAt = ts(now)
	r.Fingerprint = KeyFingerprint(r.PublicKey)
	if r.UsedBy == nil {
		r.UsedBy = []string{}
	}
	if actor != "" {
		r.CreatedBy = &actor
	}
	_, err := ex.ExecContext(ctx, `INSERT INTO backup_recipients (id, name, public_key, holder, created_by, created_at) VALUES (?,?,?,?,?,?)`,
		r.ID, r.Name, r.PublicKey, r.Holder, nullStr(actor), r.CreatedAt)
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrDuplicateRecipient
	}
	return err
}

// DeleteRecipient removes a backup key no plan uses. A key a plan still
// encrypts to returns ErrRecipientInUse with the plans named: change the
// plan first, so no plan is left encrypting to nobody.
func DeleteRecipient(ctx context.Context, db *sql.DB, ex execer, id string) ([]string, error) {
	r, err := GetRecipient(ctx, db, id)
	if err != nil {
		return nil, err
	}
	plans, err := ListPlans(ctx, db)
	if err != nil {
		return nil, err
	}
	if used := plansUsing(plans, r.ID, r.PublicKey); len(used) > 0 {
		return used, ErrRecipientInUse
	}
	if _, err := ex.ExecContext(ctx, `DELETE FROM backup_recipients WHERE id = ?`, id); err != nil {
		return nil, err
	}
	return nil, nil
}
