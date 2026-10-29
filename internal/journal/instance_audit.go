package journal

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// InstanceAuditFields commits to the stored row, not live user/email joins.
// Nullable fields retain their validity bits; NULL is distinct from empty TEXT.
type InstanceAuditFields struct {
	ID, Action, EntityType, CreatedAt                                   string
	UserID, EntityID, TargetWorkspaceID, Metadata, IPAddress, UserAgent sql.NullString
}

// InstanceAuditMAC shares the installation's stable journal key, with a separate
// subkey/domain. It is a row MAC, not a proof against whole-database rollback or
// tail truncation. Plaintext dev mode has the journal's documented weak key.
func InstanceAuditMAC(seq int64, prev string, f InstanceAuditFields) string {
	return instanceAuditMACWithKey(ChainKeyFromEnv(), seq, prev, f)
}

func instanceAuditMACWithKey(key []byte, seq int64, prev string, f InstanceAuditFields) string {
	derive := hmac.New(sha256.New, key)
	writeFramed(derive, "crewship instance audit v1")
	mac := hmac.New(sha256.New, derive.Sum(nil))
	for _, v := range []string{strconv.FormatInt(seq, 10), prev, f.ID, f.Action, f.EntityType, f.CreatedAt} {
		writeFramed(mac, v)
	}
	for _, v := range []sql.NullString{f.UserID, f.EntityID, f.TargetWorkspaceID, f.Metadata, f.IPAddress, f.UserAgent} {
		if v.Valid {
			writeFramed(mac, "text")
			writeFramed(mac, v.String)
		} else {
			writeFramed(mac, "null")
		}
	}
	return hex.EncodeToString(mac.Sum(nil))
}

// InstanceAuditFallbackKey/Value mark an instance audit row written outside the
// chain. Explicit revocations, suspension, host-local password recovery, and
// audit incident or workspace reseal notices may use it only while the chain
// tail is broken (see appendInstanceAuditTx in internal/api). Web password
// change/reset and access-granting operations remain chained-only. The row has no
// chain columns, so it neither extends nor launders the chain; verification
// counts it in UnchainedFallback and never reports such a chain as OK.
const (
	InstanceAuditFallbackKey   = "audit_chain"
	InstanceAuditFallbackValue = "unchained_fallback"
)

// IsInstanceAuditFallback reports whether metadata carries the fallback mark.
func IsInstanceAuditFallback(metadata sql.NullString) bool {
	if !metadata.Valid {
		return false
	}
	var mark map[string]any
	if json.Unmarshal([]byte(metadata.String), &mark) != nil {
		return false
	}
	return mark[InstanceAuditFallbackKey] == InstanceAuditFallbackValue
}
