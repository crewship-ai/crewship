package backup

// Validation of credentials.security_level as a bundle is applied (#1603).
//
// RestoreDump's documented guarantee is about column *names*: they are
// whitelisted against the target schema via PRAGMA table_info and quoted
// before being concatenated into SQL, so a tampered dump.json cannot smuggle
// an identifier. Values were never in scope, and for almost every column that
// is the right call — a restore re-asserts the bundle's truth, and second-
// guessing the operator's data is not the backup layer's job.
//
// security_level is the exception, because it is not data: it is the input to
// a security control. Every other writer of the column checks it against the
// tier table — the POST/PATCH handlers in internal/api/credentials_mutate.go,
// both CLI paths, and the agent-proposed-credential INSERT which hardcodes L1.
// The column is INTEGER NOT NULL DEFAULT 1 with no CHECK constraint, so
// restore was the one path by which a value the tier table has never heard of
// could reach the database.
//
// CLAMP, NOT REJECT. Failing the whole restore on a bad level would turn a
// disaster-recovery bundle into a brick over one integer, and a restore that
// drops the credential instead is worse still — the admin recovers an instance
// that is quietly missing a secret. Both trade a real, common failure for a
// rare one. Clamping keeps the row and reports it.
//
// CLAMP UP, NOT DOWN. The clamp target is the strictest tier, which is also
// what keeper.SecurityLevel.Tier() already resolves an unknown level to. So
// this changes no gate's behaviour today: an out-of-range row was already
// being treated as maximally strict at every decision point. What it changes
// is that the stored value now says so, instead of the database holding one
// number while every reader computes another.

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/crewship-ai/crewship/internal/keeper"
)

const (
	// credentialsTable is the one table whose security_level column this
	// guard applies to. Named rather than inlined so the check cannot
	// accidentally start rewriting a same-named column on another table.
	credentialsTable = "credentials"
	// securityLevelColumn is the column under guard.
	securityLevelColumn = "security_level"
	// maxSecurityLevelClampsReported bounds the per-row detail carried out
	// of a restore. The count is always exact; only the sample is capped.
	maxSecurityLevelClampsReported = 20
)

// SecurityLevelClamp records one credentials row whose bundle-supplied
// security_level was rewritten on the way in. Carried out through
// RestoreStats / RestoreResult so the CLI, the API response and the audit
// journal all report the same thing — an admin who is not told cannot act.
type SecurityLevelClamp struct {
	CredentialID string `json:"credential_id"`
	Name         string `json:"name,omitempty"`
	// From is the raw value the bundle carried, rendered for humans. A
	// string because "what the bundle said" may not be a number at all.
	From string `json:"from"`
	// To is the tier the row was written at.
	To int `json:"to"`
}

// strictestSecurityLevel is the tier an unrecognised level is clamped to.
//
// Derived from keeper.SecurityLevels() (documented ascending) rather than
// written as a literal 4, for the same reason #1557 replaced the hardcoded
// `>= L3` with a tier-table lookup: a rule spelled out independently in a
// second place is a rule that will eventually be spelled two ways, and here
// the drift would silently land credentials at a laxer tier than intended.
func strictestSecurityLevel() keeper.SecurityLevel {
	levels := keeper.SecurityLevels()
	if len(levels) == 0 {
		// Unreachable with the table as it stands. If it ever is reachable,
		// there is no tier to clamp to and the honest answer is the value
		// Tier() itself falls back to.
		return keeper.SecurityLevelL4
	}
	return levels[len(levels)-1]
}

// parseSecurityLevel reads a bundle value as a tier number.
//
// The `any` is not laziness: a dump decoded from JSON yields float64, a dump
// handed straight from the collector yields int64, normalizeScan turns []byte
// into string, and a tampered bundle can carry anything at all. Anything that
// is not an exact integer is not a tier.
func parseSecurityLevel(v any) (int, bool) {
	switch n := v.(type) {
	case nil:
		return 0, false
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float32:
		return floatSecurityLevel(float64(n))
	case float64:
		return floatSecurityLevel(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return int(i), true
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return 0, false
		}
		return i, true
	case []byte:
		i, err := strconv.Atoi(strings.TrimSpace(string(n)))
		if err != nil {
			return 0, false
		}
		return i, true
	}
	return 0, false
}

// floatSecurityLevel rejects fractional and non-finite values. 2.5 is not a
// tier, and letting SQLite's INTEGER affinity round it would invent one.
func floatSecurityLevel(f float64) (int, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, false
	}
	return int(f), true
}

// clampRestoredSecurityLevel returns the value to insert for one bundle
// security_level, and whether it had to be changed.
//
// Valid levels are returned as a plain int64 rather than passed through
// verbatim. The column has INTEGER affinity so SQLite would coerce "3" anyway;
// normalising here means a restored row holds a clean tier integer regardless
// of how the bundle spelled it, which is what the eventual CHECK constraint
// will want to see.
func clampRestoredSecurityLevel(v any) (any, bool) {
	if n, ok := parseSecurityLevel(v); ok && keeper.SecurityLevel(n).Valid() {
		return int64(n), false
	}
	return int64(strictestSecurityLevel()), true
}

// renderBundleValue describes what the bundle actually carried, for the
// warning and the audit entry. "null" and "3" are both answers an admin can
// act on; "%!v(...)" is not.
func renderBundleValue(v any) string {
	switch n := v.(type) {
	case nil:
		return "null"
	case []byte:
		return strconv.Quote(string(n))
	case string:
		return strconv.Quote(n)
	}
	return fmt.Sprintf("%v", v)
}

// recordSecurityLevelClamp appends one clamp to stats, keeping the count exact
// and the sample bounded.
func recordSecurityLevelClamp(stats *RestoreStats, row map[string]any, raw any) {
	appendSecurityLevelClamp(stats, newSecurityLevelClamp(row, raw))
}

// appendSecurityLevelClamp is recordSecurityLevelClamp for a clamp that was
// built earlier — the dump path decides whether to report one only after the
// INSERT tells it the row actually landed.
func appendSecurityLevelClamp(stats *RestoreStats, c SecurityLevelClamp) {
	stats.SecurityLevelClamped++
	if len(stats.SecurityLevelClamps) >= maxSecurityLevelClampsReported {
		return
	}
	stats.SecurityLevelClamps = append(stats.SecurityLevelClamps, c)
}

func newSecurityLevelClamp(row map[string]any, raw any) SecurityLevelClamp {
	return SecurityLevelClamp{
		CredentialID: rowString(row, "id"),
		Name:         rowString(row, "name"),
		From:         renderBundleValue(raw),
		To:           int(strictestSecurityLevel()),
	}
}

// rowString reads a bundle cell as a string without asserting a type the
// bundle is not obliged to use.
func rowString(row map[string]any, key string) string {
	v, ok := row[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	}
	return fmt.Sprintf("%v", v)
}

// InspectSecurityLevels reports, without writing anything, which credentials
// rows in a bundle would be clamped by a real restore.
//
// This is what --dry-run reports from. A dry run's contract is "every
// validation ran, nothing was written, here is what would have happened", and
// "this bundle carries credentials at a tier that does not exist" is the most
// useful thing it can tell an admin before they commit to the restore.
//
// Returns the same bounded sample as a live restore, plus the exact total.
func InspectSecurityLevels(dump *DBDump) ([]SecurityLevelClamp, int) {
	if dump == nil {
		return nil, 0
	}
	var stats RestoreStats
	for _, row := range dump.Tables[credentialsTable] {
		raw, ok := row[securityLevelColumn]
		if !ok {
			// Absent column: schema skew, not tampering. The INSERT omits it
			// and the schema DEFAULT applies.
			continue
		}
		if _, clamped := clampRestoredSecurityLevel(raw); clamped {
			recordSecurityLevelClamp(&stats, row, raw)
		}
	}
	return stats.SecurityLevelClamps, stats.SecurityLevelClamped
}
