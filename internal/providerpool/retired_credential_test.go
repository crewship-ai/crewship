package providerpool

import (
	"errors"
	"testing"
	"time"
)

func TestStoreRetiredCredentialErasurePreservesPoolPolicy(t *testing.T) {
	for _, scenario := range []string{"remaining active", "all retired", "retired foreign owner", "ordinary inactive missing mode", "active invalid mode"} {
		t.Run(scenario, func(t *testing.T) {
			store, db, _ := poolFixture(t)
			if err := store.Create(t.Context(), fixturePool()); err != nil {
				t.Fatal(err)
			}
			if got, err := store.Choose(t.Context(), "ws", "pool", time.Now()); err != nil || got.ID != "a" {
				t.Fatalf("healthy calibration: %+v %v", got, err)
			}
			wantID, wantErr := "a", error(nil)
			switch scenario {
			case "remaining active", "all retired", "retired foreign owner":
				if scenario == "retired foreign owner" {
					execPoolSQL(t, db, `UPDATE credentials SET created_by='other' WHERE id='b'`)
					wantID, wantErr = "", ErrCrossOwner
				}
				execPoolSQL(t, db, `UPDATE credentials SET deleted_at='2026-10-19T00:00:00Z' WHERE id='b'`)
				var erased bool
				if err := db.QueryRow(`SELECT encrypted_value='' AND NOT EXISTS(SELECT 1 FROM credential_fields WHERE credential_id='b') FROM credentials WHERE id='b'`).Scan(&erased); err != nil || !erased {
					t.Fatalf("native scrub calibration: erased=%v err=%v", erased, err)
				}
				if scenario == "all retired" {
					execPoolSQL(t, db, `UPDATE credentials SET deleted_at='2026-10-19T00:00:00Z' WHERE id='a'`)
					wantID, wantErr = "", ErrUnavailable
				}
			case "ordinary inactive missing mode":
				execPoolSQL(t, db, `UPDATE credentials SET status='EXPIRED' WHERE id='b'`)
				execPoolSQL(t, db, `DELETE FROM credential_fields WHERE credential_id='b' AND key='mode'`)
				wantID, wantErr = "", ErrInvalid
			case "active invalid mode":
				execPoolSQL(t, db, `UPDATE credential_fields SET value='api_key' WHERE credential_id='b' AND key='mode'`)
				wantID, wantErr = "", ErrInvalid
			}
			got, err := store.Choose(t.Context(), "ws", "pool", time.Now())
			if !errors.Is(err, wantErr) || got.ID != wantID {
				t.Fatalf("post-erasure selection: id=%q err=%v; want id=%q err=%v", got.ID, err, wantID, wantErr)
			}
		})
	}
}
