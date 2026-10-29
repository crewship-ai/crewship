package journal

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func TestAuditReplayRetainsReceiptSigningIdentityAcrossRotation(t *testing.T) {
	for _, legacy := range []bool{true, false} {
		name := "versioned"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			oldKey := DeriveChainKey("synthetic-replay-before")
			nextKey := DeriveChainKey("synthetic-replay-after")
			makeKeys := func(active string) *SigningKeys {
				raw, err := json.Marshal(signingKeyDocument{ActiveID: active, Keys: []signingKeyRecord{
					{ID: LegacySigningKeyID, Key: base64.StdEncoding.EncodeToString(oldKey)},
					{ID: "before", Key: base64.StdEncoding.EncodeToString(oldKey)},
					{ID: "after", Key: base64.StdEncoding.EncodeToString(nextKey)},
				}})
				if err != nil {
					t.Fatal(err)
				}
				keys, err := ParseSigningKeys(raw)
				if err != nil {
					t.Fatal(err)
				}
				return keys
			}
			oldKeys := makeKeys("before")
			if legacy {
				oldKeys = LegacySigningKeys(oldKey)
			}
			old := NewWriter(db, quietLogger(), WriterOptions{SigningKeys: oldKeys})
			entry := replayEntryFixture()
			if _, inserted, err := old.EmitReplaySync(t.Context(), entry); err != nil || !inserted {
				t.Fatalf("healthy original receipt: inserted=%v err=%v", inserted, err)
			}
			old.Close()
			var before string
			if err := db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, entry.ID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			current := NewWriter(db, quietLogger(), WriterOptions{SigningKeys: makeKeys("after")})
			defer current.Close()
			notify := current.Notify()
			if _, inserted, err := current.EmitReplaySync(t.Context(), entry); err != nil || inserted {
				t.Errorf("retained receipt key did not deduplicate replay after rotation: inserted=%v err=%v", inserted, err)
			}
			select {
			case <-notify:
				t.Error("duplicate replay emitted notification")
			default:
			}
			var after string
			var count int
			if err := db.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id=?`, entry.ID).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE id=?`, entry.ID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if before != after || count != 1 {
				t.Errorf("replay rewrote receipt or duplicated row: same=%v count=%d", before == after, count)
			}
		})
	}
}
