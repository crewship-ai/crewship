package backup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// Vault keys and the recovery kit.
//
// Every sealed value in the database is an AES-256-GCM envelope stamped with
// the key version that sealed it ("v1:…", "v2:…"). The keys themselves live in
// the environment (ENCRYPTION_KEY, ENCRYPTION_KEY_V2, …), never in the
// database, so a backup of the database alone restores credentials nobody can
// read. The recovery kit closes that gap for INSTANCE bundles only, and only
// when an instance admin turned it on: every key version an envelope in the
// database references rides inside the age-encrypted payload as
// recovery-kit/keys.json. Whoever holds a private key the bundle is encrypted
// to can then read every secret in it — which is why it is off by default and
// never part of a workspace or crew bundle.

// RecoveryKitPath is the payload entry holding the vault keys.
const RecoveryKitPath = "recovery-kit/keys.json"

// IncompleteVaultKeyMissing: envelopes in the database reference a key
// version this server could not resolve, so the recovery kit cannot carry it.
const IncompleteVaultKeyMissing = "vault_key_missing"

// RecoveryKit is recovery-kit/keys.json.
type RecoveryKit struct {
	Versions []RecoveryKitKey `json:"versions"`
	// CurrentVersion is the version new envelopes were minted with on the
	// source (CREWSHIP_ENCRYPTION_KEY_VERSION, default v1).
	CurrentVersion string    `json:"current_version,omitempty"`
	GeneratedAt    time.Time `json:"generated_at"`
}

// RecoveryKitKey is one key version. KeyB64 is the raw 32-byte key, base64.
// EnvValue is the variable's exact string on the source, which the restored
// server must be given byte for byte: the journal chain key is derived from
// the ENCRYPTION_KEY string itself, so a re-encoded hex (different case)
// decrypts every credential and still breaks chain verification.
type RecoveryKitKey struct {
	Version  string `json:"version"`
	Env      string `json:"env"`
	KeyB64   string `json:"key_b64"`
	EnvValue string `json:"env_value,omitempty"`
}

// envValue is what the restored environment variable must hold.
func (k RecoveryKitKey) envValue() (string, error) {
	if k.EnvValue != "" {
		return k.EnvValue, nil
	}
	raw, err := k.Raw()
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// ChainSeed is the ENCRYPTION_KEY string the journal chain key derives from,
// or "" when the kit has no v1 key.
func (k *RecoveryKit) ChainSeed() string {
	if k == nil {
		return ""
	}
	for _, v := range k.Versions {
		if v.Version == "v1" {
			s, _ := v.envValue()
			return s
		}
	}
	return ""
}

// Raw decodes the key.
func (k RecoveryKitKey) Raw() ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(k.KeyB64)
	if err != nil {
		return nil, fmt.Errorf("backup: recovery kit key %s: %w", k.Version, err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("backup: recovery kit key %s is %d bytes, want 32", k.Version, len(b))
	}
	return b, nil
}

// KeyMap returns version → raw key for DecryptWithKeys.
func (k *RecoveryKit) KeyMap() (map[string][]byte, error) {
	out := map[string][]byte{}
	if k == nil {
		return out, nil
	}
	for _, v := range k.Versions {
		raw, err := v.Raw()
		if err != nil {
			return nil, err
		}
		out[v.Version] = raw
	}
	return out, nil
}

// VaultScan counts the envelopes in the database per key version.
type VaultScan struct {
	// Versions maps key version → envelopes sealed with it.
	Versions map[string]int
	// Plaintext counts non-empty values in fail-open columns that are not
	// envelopes (key-less legacy state; nothing to unlock).
	Plaintext int
}

// Total is the number of envelopes.
func (s VaultScan) Total() int {
	n := 0
	for _, c := range s.Versions {
		n += c
	}
	return n
}

// SealedValue is one envelope in the database, for a drill to unlock.
type SealedValue struct {
	Table, Column string
	RowID         int64
	Envelope      string
}

type rowQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// EachSealedValue walks every enveloped value in encryption.EnvelopeColumns.
// A table or column missing from this schema is skipped.
func EachSealedValue(ctx context.Context, db rowQuerier, fn func(SealedValue) error) (plaintext int, err error) {
	for _, col := range encryption.EnvelopeColumns {
		if !validSQLIdent.MatchString(col.Table) || !validSQLIdent.MatchString(col.Column) {
			return plaintext, fmt.Errorf("backup: envelope inventory names an invalid column %s.%s", col.Table, col.Column)
		}
		q := `SELECT rowid, ` + col.Column + ` FROM ` + col.Table + ` WHERE ` + col.Column + ` IS NOT NULL AND ` + col.Column + ` != ''` // nosemgrep: gosql-sqli — identifiers are compile-time constants validated above
		if col.Where != "" {
			q += ` AND (` + col.Where + `)`
		}
		rows, qerr := db.QueryContext(ctx, q)
		if qerr != nil {
			msg := strings.ToLower(qerr.Error())
			if strings.Contains(msg, "no such table") || strings.Contains(msg, "no such column") {
				continue
			}
			return plaintext, fmt.Errorf("backup: scan %s.%s: %w", col.Table, col.Column, qerr)
		}
		for rows.Next() {
			var id int64
			var v string
			if err := rows.Scan(&id, &v); err != nil {
				_ = rows.Close()
				return plaintext, fmt.Errorf("backup: scan %s.%s: %w", col.Table, col.Column, err)
			}
			env := strings.TrimPrefix(v, col.Prefix)
			if _, ok := encryption.ParseEnvelopeVersion(env); !ok {
				plaintext++
				continue
			}
			if err := fn(SealedValue{Table: col.Table, Column: col.Column, RowID: id, Envelope: env}); err != nil {
				_ = rows.Close()
				return plaintext, err
			}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return plaintext, err
		}
		_ = rows.Close()
	}
	return plaintext, nil
}

// ScanVaultEnvelopes counts envelopes per key version across the inventory.
func ScanVaultEnvelopes(ctx context.Context, db rowQuerier) (VaultScan, error) {
	scan := VaultScan{Versions: map[string]int{}}
	plain, err := EachSealedValue(ctx, db, func(v SealedValue) error {
		ver, _ := encryption.ParseEnvelopeVersion(v.Envelope)
		scan.Versions[ver]++
		return nil
	})
	scan.Plaintext = plain
	return scan, err
}

// BuildRecoveryKit resolves the key for every version the scan found (and
// the current mint version), exactly as Decrypt would on this server. A
// version with envelopes and no resolvable key comes back in missing, with
// its envelope count, so the bundle records it as incomplete.
func BuildRecoveryKit(scan VaultScan, now time.Time) (*RecoveryKit, map[string]int, error) {
	kit := &RecoveryKit{GeneratedAt: now.UTC()}
	missing := map[string]int{}
	cur, err := encryption.CurrentKeyVersion()
	if err != nil {
		return nil, nil, err
	}
	kit.CurrentVersion = cur
	want := map[string]bool{cur: true}
	for v, n := range scan.Versions {
		if n > 0 {
			want[v] = true
		}
	}
	versions := make([]string, 0, len(want))
	for v := range want {
		versions = append(versions, v)
	}
	encryption.SortVersions(versions)
	for _, v := range versions {
		rk, err := encryption.ResolveKey(v)
		if err != nil {
			if n := scan.Versions[v]; n > 0 {
				missing[v] = n
			}
			continue
		}
		kit.Versions = append(kit.Versions, RecoveryKitKey{
			Version:  v,
			Env:      encryption.KeyEnvVar(v),
			KeyB64:   base64.StdEncoding.EncodeToString(rk.Key),
			EnvValue: rk.Value,
		})
	}
	return kit, missing, nil
}

// ParseRecoveryKit reads keys.json and checks every key decodes.
func ParseRecoveryKit(data []byte) (*RecoveryKit, error) {
	var kit RecoveryKit
	if err := json.Unmarshal(data, &kit); err != nil {
		return nil, fmt.Errorf("backup: parse recovery kit: %w", err)
	}
	for _, v := range kit.Versions {
		if !encryption.ValidKeyVersion(v.Version) {
			return nil, fmt.Errorf("backup: recovery kit names %q, not a key version", v.Version)
		}
		if v.Env != encryption.KeyEnvVar(v.Version) {
			return nil, fmt.Errorf("backup: recovery kit maps %s to %q", v.Version, v.Env)
		}
		raw, err := v.Raw()
		if err != nil {
			return nil, err
		}
		if v.EnvValue != "" {
			b, err := hex.DecodeString(v.EnvValue)
			if err != nil || string(b) != string(raw) {
				return nil, fmt.Errorf("backup: recovery kit key %s: env_value does not match the key", v.Version)
			}
		}
	}
	return &kit, nil
}

// EnvLines renders the kit as environment assignments (hex keys, as the
// server reads them), newest mint version last.
func (k *RecoveryKit) EnvLines() ([]string, error) {
	if k == nil {
		return nil, errors.New("backup: no recovery kit")
	}
	var out []string
	for _, v := range k.Versions {
		val, err := v.envValue()
		if err != nil {
			return nil, err
		}
		out = append(out, v.Env+"="+val)
	}
	if k.CurrentVersion != "" && k.CurrentVersion != "v1" {
		out = append(out, encryption.KeyVersionEnvVar+"="+k.CurrentVersion)
	}
	return out, nil
}

// RecoveryKitEnabled reads backup_settings.recovery_kit_enabled. A missing
// table or row reads as off.
func RecoveryKitEnabled(ctx context.Context, db *sql.DB) (bool, error) {
	var on int
	err := db.QueryRowContext(ctx, `SELECT recovery_kit_enabled FROM backup_settings WHERE id = 1`).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return false, nil
		}
		return false, fmt.Errorf("backup: read recovery kit setting: %w", err)
	}
	return on == 1, nil
}

// SetRecoveryKitEnabled writes the switch in ex (the caller's transaction,
// which also writes the audit entry).
func SetRecoveryKitEnabled(ctx context.Context, ex catalogExecer, enabled bool, actor string, at time.Time) error {
	_, err := ex.ExecContext(ctx, `
INSERT INTO backup_settings (id, recovery_kit_enabled, updated_at, updated_by) VALUES (1, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET recovery_kit_enabled = excluded.recovery_kit_enabled,
  updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
		boolToInt(enabled), at.UTC().Format(time.RFC3339), nullableString(actor))
	if err != nil {
		return fmt.Errorf("backup: set recovery kit: %w", err)
	}
	return nil
}
