package backup

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

func TestRestoreDumpImageSelectionOwnershipAndExclusion(t *testing.T) {
	for _, state := range []string{"local", "foreign", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			source := openMigratedDBCov(t)
			ws, crew := seedCovWorkspace(t, source, "image-selection")
			if _, err := source.Exec(`UPDATE crews SET cached_image='synthetic-image',previous_image='synthetic-previous' WHERE id=?`, crew); err != nil {
				t.Fatal(err)
			}
			dump, err := DumpWorkspace(t.Context(), source, ws)
			if err != nil {
				t.Fatal(err)
			}
			target := openMigratedDBCov(t)
			gate := resourcelifecycle.Images(target)
			gate.SetSelectionCheck(func(context.Context, string) (bool, error) {
				if state == "unavailable" {
					return false, errors.New("daemon unavailable")
				}
				return state == "local", nil
			})
			checked := false
			_, err = RestoreDumpTxHooks(t.Context(), target, dump, &RestoreDumpHooks{PreCommit: func(context.Context) error {
				if release, ok := gate.TryCollect(); ok {
					release()
					t.Fatal("collector admitted during restore")
				}
				checked = true
				return nil
			}})
			if state == "unavailable" {
				if err == nil {
					t.Fatal("restore accepted unknown ownership")
				}
				return
			}
			if err != nil || !checked {
				t.Fatalf("restore failed: %v checked=%t", err, checked)
			}
			var current, previous sql.NullString
			if err := target.QueryRow(`SELECT cached_image,previous_image FROM crews WHERE id=?`, crew).Scan(&current, &previous); err != nil {
				t.Fatal(err)
			}
			if state == "local" && (current.String != "synthetic-image" || previous.String != "synthetic-previous") {
				t.Fatal("local selections lost")
			}
			if state == "foreign" && (current.Valid || previous.Valid) {
				t.Fatal("foreign selections adopted")
			}
		})
	}
}
