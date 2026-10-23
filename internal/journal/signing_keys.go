package journal

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"sort"
)

const SigningKeysPathEnv = "CREWSHIP_AUDIT_KEYRING_PATH"
const LegacySigningKeyID = "legacy-v1"

// SigningKeys is an immutable operation/boot snapshot. Retained HMAC keys are
// sensitive verification keys, not public keys. Only activeID may sign v2 rows.
// No key bytes are exported or marshaled by this type.
type SigningKeys struct {
	activeID string
	keys     map[string][]byte
}
type signingKeyDocument struct {
	ActiveID string             `json:"active_key_id"`
	Keys     []signingKeyRecord `json:"keys"`
}
type signingKeyRecord struct {
	ID  string `json:"id"`
	Key string `json:"key_b64"`
}

var signingKeyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var ErrSigningKeyUnavailable = errors.New("audit signing key unavailable")
var ErrChainIntegrity = errors.New("audit chain integrity failure")

func LegacySigningKeys(key []byte) *SigningKeys {
	return &SigningKeys{keys: map[string][]byte{LegacySigningKeyID: append([]byte(nil), key...)}}
}
func (k *SigningKeys) ActiveID() string {
	if k == nil {
		return ""
	}
	return k.activeID
}
func (k *SigningKeys) active() (string, int, []byte, error) {
	if k == nil {
		return "", 0, nil, ErrSigningKeyUnavailable
	}
	id, version := k.activeID, 2
	if id == "" {
		id, version = LegacySigningKeyID, 0
	}
	key, ok := k.keys[id]
	if !ok {
		return "", 0, nil, ErrSigningKeyUnavailable
	}
	if version == 0 {
		id = ""
	}
	return id, version, key, nil
}
func (k *SigningKeys) verification(id sql.NullString, version sql.NullInt64) ([]byte, error) {
	name := id.String
	if !id.Valid && !version.Valid {
		name = LegacySigningKeyID
	} else if !id.Valid || !version.Valid || version.Int64 != 2 || name == "" {
		return nil, ErrChainIntegrity
	}
	if k == nil {
		return nil, ErrSigningKeyUnavailable
	}
	key, ok := k.keys[name]
	if !ok {
		return nil, ErrSigningKeyUnavailable
	}
	return key, nil
}
func ParseSigningKeys(data []byte) (*SigningKeys, error) {
	var document signingKeyDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("audit keyring: invalid document")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("audit keyring: trailing data")
	}
	if !signingKeyID.MatchString(document.ActiveID) || document.ActiveID == LegacySigningKeyID || len(document.Keys) == 0 || len(document.Keys) > 128 {
		return nil, errors.New("audit keyring: invalid active ID or key count")
	}
	keys := &SigningKeys{activeID: document.ActiveID, keys: map[string][]byte{}}
	for _, record := range document.Keys {
		if !signingKeyID.MatchString(record.ID) {
			return nil, errors.New("audit keyring: invalid key ID")
		}
		if _, exists := keys.keys[record.ID]; exists {
			return nil, errors.New("audit keyring: duplicate key ID")
		}
		key, err := base64.StdEncoding.DecodeString(record.Key)
		if err != nil || len(key) != 32 {
			return nil, errors.New("audit keyring: keys must contain 32 bytes")
		}
		keys.keys[record.ID] = key
	}
	if _, exists := keys.keys[keys.activeID]; !exists {
		return nil, ErrSigningKeyUnavailable
	}
	return keys, nil
}
func LoadSigningKeys(path string) (*SigningKeys, error) {
	if path == "" {
		return defaultSigningKeys(), nil
	}
	raw, err := readSigningKeysFile(path)
	if err != nil {
		return nil, err
	}
	plain, _, err := openSigningKeys(raw)
	if err != nil {
		return nil, err
	}
	return ParseSigningKeys(plain)
}

// readSigningKeysFile reads a bounded private regular file, sealed or not.
func readSigningKeysFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() {
		return nil, errors.New("audit keyring: private regular file required")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("audit keyring: cannot open file")
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !os.SameFile(before, actual) || (runtime.GOOS != "windows" && actual.Mode().Perm()&0077 != 0) {
		return nil, errors.New("audit keyring: private file permissions required")
	}
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil || len(data) > 64*1024 {
		return nil, errors.New("audit keyring: cannot read bounded document")
	}
	return data, nil
}
func LoadSigningKeysFromEnv() (*SigningKeys, error) {
	return LoadSigningKeys(os.Getenv(SigningKeysPathEnv))
}

type signingKeysContextKey struct{}

func WithSigningKeys(ctx context.Context, keys *SigningKeys) context.Context {
	return context.WithValue(ctx, signingKeysContextKey{}, keys)
}
func SigningKeysForContext(ctx context.Context) (*SigningKeys, error) {
	if keys, ok := ctx.Value(signingKeysContextKey{}).(*SigningKeys); ok {
		if keys == nil {
			return nil, ErrSigningKeyUnavailable
		}
		return keys, nil
	}
	return LoadSigningKeysFromEnv()
}

// ExportSigningKeys is exclusively for the age-encrypted instance RecoveryKit.
// Workspace/crew dumps must never call it. The result contains secrets.
func ExportSigningKeys(keys *SigningKeys) ([]byte, error) {
	if keys == nil {
		return nil, ErrSigningKeyUnavailable
	}
	if keys.activeID == "" {
		legacy, err := keys.verification(sql.NullString{}, sql.NullInt64{})
		if err != nil {
			return nil, err
		}
		keys = BootstrapSigningKeys(legacy)
	}
	document := signingKeyDocument{ActiveID: keys.activeID}
	ids := make([]string, 0, len(keys.keys))
	for id := range keys.keys {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		document.Keys = append(document.Keys, signingKeyRecord{ID: id, Key: base64.StdEncoding.EncodeToString(keys.keys[id])})
	}
	return json.Marshal(document)
}
func versionedMAC(key []byte, domain, id, content string) string {
	mac := hmac.New(sha256.New, key)
	for _, field := range []string{domain, "2", id, content} {
		writeFramed(mac, field)
	}
	return hex.EncodeToString(mac.Sum(nil))
}
func chainHashForKeys(keys *SigningKeys, prev string, f ChainFields) (string, error) {
	key, err := keys.verification(f.KeyID, f.MACVersion)
	if err != nil {
		return "", err
	}
	legacy := ChainHashKeyed(key, prev, f)
	if !f.KeyID.Valid {
		return legacy, nil
	}
	return versionedMAC(key, "crewship workspace audit row", f.KeyID.String, legacy), nil
}
func InstanceAuditMACForKeys(keys *SigningKeys, id sql.NullString, version sql.NullInt64, seq int64, prev string, f InstanceAuditFields) (string, error) {
	key, err := keys.verification(id, version)
	if err != nil {
		return "", err
	}
	legacy := instanceAuditMACWithKey(key, seq, prev, f)
	if !id.Valid {
		return legacy, nil
	}
	return versionedMAC(key, "crewship instance audit row", id.String, legacy), nil
}
func keyColumns(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, table string) (bool, error) {
	var present int
	err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name IN ('chain_key_id','chain_mac_version')`, table).Scan(&present)
	if err != nil {
		return false, err
	}
	if present != 0 && present != 2 {
		return false, fmt.Errorf("audit: incomplete versioned schema")
	}
	return present == 2, nil
}

// ActiveSigningIdentity is public metadata only, never key material.
func ActiveSigningIdentity(keys *SigningKeys) (sql.NullString, sql.NullInt64) {
	if keys == nil || keys.activeID == "" {
		return sql.NullString{}, sql.NullInt64{}
	}
	return sql.NullString{String: keys.activeID, Valid: true}, sql.NullInt64{Int64: 2, Valid: true}
}

// ChainHashForKeys is shared with the explicit, provenance-bearing fork restore.
func ChainHashForKeys(keys *SigningKeys, prev string, fields ChainFields) (string, error) {
	return chainHashForKeys(keys, prev, fields)
}
