package journal

import (
	"fmt"
	"testing"
)

// BenchmarkAuditAppendHistory measures one transactional append against a
// pre-seeded history. Append admission checks only the chain tail, so the cost
// must stay flat as the history grows. Rollback keeps the history length
// fixed; setup uses one real writer batch.
func BenchmarkAuditAppendHistory(b *testing.B) {
	for _, size := range []int{100, 10000, 100000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			db := openTestDB(b)
			defer db.Close()
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			batch := make([]Entry, size)
			for i := range batch {
				entry, err := prepareEntry(b.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Synthetic history"})
				if err != nil {
					b.Fatal(err)
				}
				batch[i] = entry
			}
			if err := writer.persistBatch(b.Context(), batch); err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for b.Loop() {
				tx, err := db.BeginTx(b.Context(), nil)
				if err != nil {
					b.Fatal(err)
				}
				_, err = writer.AppendTx(b.Context(), tx, Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Audited append"})
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
