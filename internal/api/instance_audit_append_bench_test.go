package api

import (
	"database/sql"
	"fmt"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// seedInstanceAuditHistory writes size authentic chained rows in one
// transaction, computing each MAC directly so setup stays linear.
func seedInstanceAuditHistory(tb testing.TB, db *sql.DB, keys *journal.SigningKeys, size int) {
	tb.Helper()
	tx, err := db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck
	keyID, version := journal.ActiveSigningIdentity(keys)
	prev := ""
	for seq := int64(1); seq <= int64(size); seq++ {
		f := journal.InstanceAuditFields{ID: fmt.Sprintf("seed-%07d", seq), Action: "fixture.history", EntityType: "instance", CreatedAt: "2026-10-01T00:00:00.000Z"}
		hash, err := journal.InstanceAuditMACForKeys(keys, keyID, version, seq, prev, f)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) VALUES(?,?,?,?,?,?,?,?,?)`, f.ID, f.Action, f.EntityType, f.CreatedAt, seq, prev, hash, keyID, version); err != nil {
			tb.Fatal(err)
		}
		prev = hash
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkInstanceAuditAppend measures one instance audit append against a
// pre-seeded chain. Admission checks only the tail, so the cost must stay flat
// as the history grows.
func BenchmarkInstanceAuditAppend(b *testing.B) {
	for _, size := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			db := testutil.MigratedSQLDB(b)
			keys, err := journal.SigningKeysForContext(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			seedInstanceAuditHistory(b, db, keys, size)
			b.ResetTimer()
			for b.Loop() {
				tx, err := db.BeginTx(b.Context(), nil)
				if err != nil {
					b.Fatal(err)
				}
				err = appendInstanceAuditTx(b.Context(), tx, journal.InstanceAuditFields{ID: generateCUID(), Action: "fixture.append", EntityType: "instance", CreatedAt: "2026-10-01T00:00:00.000Z"})
				rollbackErr := tx.Rollback()
				if err != nil {
					b.Fatal(err)
				}
				if rollbackErr != nil {
					b.Fatal(rollbackErr)
				}
			}
		})
	}
}
