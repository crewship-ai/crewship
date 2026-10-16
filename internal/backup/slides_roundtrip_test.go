package backup_test

import (
	"context"
	"database/sql"
	"errors"
	"filippo.io/age"
	"fmt"
	"github.com/crewship-ai/crewship/internal/database"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/slides"
)

func backupSlideAuthority(context.Context, *sql.Tx) (string, error) { return "user:u_admin", nil }
func seedBackupSlides(t *testing.T, db *sql.DB, ws, page, owner string) (slides.Deck, error) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,?,?,?,'{}')`, page, ws, page, page, owner); err != nil {
		return slides.Deck{}, err
	}
	deck := slides.Deck{SchemaVersion: 1, DeckID: page, Width: 960, Height: 540, Theme: slides.Theme{Background: "#ffffff", Color: "#000000", FontFamily: "Arial", FontSize: 24}, SlideOrder: []string{"slide_one", "slide_two"}, Slides: []slides.Slide{{ID: "slide_one", Notes: "český text\nLF", Elements: []slides.Element{{ID: "deleted_element", Type: "shape", X: 0, Y: 0, Width: 50, Height: 50, Shape: &slides.Shape{Kind: "rectangle", Fill: "#ffffff", Stroke: "#000000", StrokeWidth: 1}}}}, {ID: "slide_two", Notes: "", Elements: []slides.Element{}}}}
	store := slides.New(db, slides.Options{})
	key := slides.Key{WorkspaceID: ws, PageID: page}
	if _, err := store.Initialize(t.Context(), key, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "original_initialize", Deck: deck}, backupSlideAuthority); err != nil {
		return deck, err
	}
	hash, _ := slides.ElementHash(deck.Slides[0].Elements[0])
	_, err := store.Patch(t.Context(), key, slides.PatchRequest{PageID: page, SchemaVersion: 1, BaseRev: 1, MutationID: "original_delete", Operations: []slides.Operation{{Op: "delete_element", SlideID: "slide_one", ElementID: "deleted_element", IfHash: hash}}}, backupSlideAuthority)
	return deck, err
}
func TestSlidesEncryptedBackupForkHistoryRestore(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	original, err := seedBackupSlides(t, source, ws, "slides-page", "u_admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('foreign-slides','Foreign','foreign-slides')`); err != nil {
		t.Fatal(err)
	}
	if _, err = seedBackupSlides(t, source, "foreign-slides", "foreign-slide-page", "u_admin"); err != nil {
		t.Fatal(err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	bundle, err := backup.CreateBackup(t.Context(), source, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Passphrase: "slides-backup-roundtrip-passphrase", Actor: actor})
	if err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same", true: "fork"}[fork], func(t *testing.T) {
			target := openMigratedDB(t)
			options := backup.RestoreOptions{Path: bundle.Path, Passphrase: "slides-backup-roundtrip-passphrase", Actor: actor}
			if fork {
				options.AsWorkspace = "slides-fork"
			}
			if _, err := backup.RestoreBackup(t.Context(), target, options); err != nil {
				t.Fatal(err)
			}
			var key slides.Key
			var count int
			if err := target.QueryRow(`SELECT workspace_id,page_id FROM slides_heads`).Scan(&key.WorkspaceID, &key.PageID); err != nil {
				t.Fatal(err)
			}
			if fork && (key.WorkspaceID == ws || key.PageID == "slides-page") {
				t.Fatal("fork did not remap namespace")
			}
			if !fork && (key.WorkspaceID != ws || key.PageID != "slides-page") {
				t.Fatal("same-identity restore changed namespace")
			}
			store := slides.New(target, slides.Options{})
			for _, revision := range []int64{0, 1, 2} {
				state, err := store.Read(t.Context(), key, revision, backupSlideAuthority)
				if err != nil || state.Deck.DeckID != key.PageID {
					t.Fatalf("restored canonical read rev%d: %#v %v", revision, state, err)
				}
				if state.Deck.SlideOrder[0] != "slide_one" {
					t.Fatal("stable slide IDs changed")
				}
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM slides_object_ids WHERE object_id='deleted_element'`).Scan(&count); err != nil || count != 1 {
				t.Fatal("deleted identity tombstone lost", err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM slides_heads`).Scan(&count); err != nil || count != 1 {
				t.Fatal("foreign workspace leaked", err)
			}
			if err := target.QueryRow(`SELECT COUNT(*) FROM slides_receipts`).Scan(&count); err != nil || count != map[bool]int{false: 2, true: 0}[fork] {
				t.Fatalf("fork receipt policy count%d %v", count, err)
			}
			original.DeckID = key.PageID
			replay, err := store.Initialize(t.Context(), key, slides.InitializeRequest{PageID: key.PageID, SchemaVersion: 1, MutationID: "original_initialize", Deck: original}, backupSlideAuthority)
			if fork {
				if !errors.Is(err, slides.ErrDuplicate) {
					t.Fatalf("historic fork mutation not409: %v", err)
				}
			} else if err != nil || !replay.Duplicate {
				t.Fatalf("same identity receipt not replayable under live authority: %#v %v", replay, err)
			}
			history, err := store.History(t.Context(), key, backupSlideAuthority)
			if err != nil || len(history) != 2 {
				t.Fatalf("history %v %v", history, err)
			}
			if _, err := store.Restore(t.Context(), key, slides.RestoreRequest{PageID: key.PageID, SchemaVersion: 1, BaseRev: 2, IfOrderVersion: 1, MutationID: "post_restore", Revision: 1}, backupSlideAuthority); err != nil {
				t.Fatal(err)
			}
			state, err := store.Read(t.Context(), key, 0, backupSlideAuthority)
			if err != nil || state.Revision != 3 || state.Deck.Slides[0].Elements[0].ID != "deleted_element" {
				t.Fatalf("original identity restore %#v %v", state, err)
			}
			// Revocation still applies after restore; a saved receipt is not authority.
			deny := func(context.Context, *sql.Tx) (string, error) { return "", errors.New("revoked") }
			if _, err := store.Read(t.Context(), key, 1, deny); err == nil {
				t.Fatal("pinned historical bytes bypassed live auth")
			}
		})
	}
}

func TestSlidesRestoreRejectsMismatchedRowSchemaAtomically(t *testing.T) {
	source := openMigratedDB(t)
	ws := seedWorkspace(t, source)
	if _, err := seedBackupSlides(t, source, ws, "schema-page", "u_admin"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"slides_heads", "slides_revisions"} {
		for _, version := range []any{float64(2), float64(1.5), nil} {
			t.Run(fmt.Sprintf("%s/%v", table, version), func(t *testing.T) {
				dump, err := backup.DumpWorkspace(t.Context(), source, ws)
				if err != nil {
					t.Fatal(err)
				}
				dump.Tables[table][0]["schema_version"] = version
				target := openMigratedDB(t)
				if err := backup.RestoreDump(t.Context(), target, dump); err == nil {
					t.Fatal("accepted row/snapshot schema mismatch")
				}
				for _, name := range []string{"pages", "slides_heads", "slides_revisions", "slides_receipts", "slides_object_ids"} {
					var count int
					if err := target.QueryRow(`SELECT COUNT(*) FROM ` + name).Scan(&count); err != nil || count != 0 {
						t.Fatalf("failed restore left %s=%d %v", name, count, err)
					}
				}
			})
		}
	}
}

func TestSlidesInstanceEncryptedRecovery(t *testing.T) {
	f := newInstanceFixture(t)
	for _, test := range []struct{ ws, page, owner string }{{"ws_e2e_1", "instance-slides", "u_admin"}, {"ws_lab", "foreign-instance-slides", "u_lab"}} {
		if _, err := seedBackupSlides(t, f.db, test.ws, test.page, test.owner); err != nil {
			t.Fatal(err)
		}
	}
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "recovered")
	if _, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}); err != nil {
		t.Fatal(err)
	}
	target, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	store := slides.New(target.DB, slides.Options{})
	for _, key := range []slides.Key{{WorkspaceID: "ws_e2e_1", PageID: "instance-slides"}, {WorkspaceID: "ws_lab", PageID: "foreign-instance-slides"}} {
		for _, rev := range []int64{0, 1, 2} {
			state, err := store.Read(t.Context(), key, rev, backupSlideAuthority)
			if err != nil || state.Deck.DeckID != key.PageID {
				t.Fatalf("instance recovery read %#v %v", state, err)
			}
		}
	}
}
