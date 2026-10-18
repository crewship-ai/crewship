package conversation

import (
	"fmt"
	"testing"
)

func TestDocumentDraftingRequiresOriginalTrustedConsent(t *testing.T) {
	for _, drafting := range []bool{false, true} {
		t.Run(fmt.Sprint(drafting), func(t *testing.T) {
			store, receipt, intent, message := creationStore(t, drafting)
			if err := store.AppendDocumentCreation(t.Context(), receipt.ChatID, message, intent, receipt); err != nil {
				t.Fatal(err)
			}
			var allowed bool
			if err := store.db.QueryRow(`SELECT drafting_allowed FROM document_creation_consents WHERE message_id=?`, message.ID).Scan(&allowed); err != nil {
				t.Fatal(err)
			}
			if allowed != drafting {
				t.Fatal("original drafting purpose was not persisted")
			}
			if _, err := store.db.Exec(`UPDATE document_creation_consents SET drafting_allowed=? WHERE message_id=?`, !drafting, message.ID); err == nil {
				t.Fatal("original purpose could be changed after admission")
			}
		})
	}
	store, receipt, _, message := creationStore(t, true)
	if err := store.Append(t.Context(), receipt.ChatID, message); err != nil {
		t.Fatal(err)
	}
	if got := creationCount(t, store.db, `SELECT COUNT(*) FROM document_creation_consents`); got != 0 {
		t.Fatal("ordinary metadata minted drafting authority")
	}
}
