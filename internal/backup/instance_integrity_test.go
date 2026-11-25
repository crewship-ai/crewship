package backup_test

import (
	"bytes"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// A matching ciphertext checksum and correct row counts can coexist with a
// damaged SQLite database. Preserve the bundle but do not award recovery proof.
func TestInstanceBackupDamagedSnapshotProof(t *testing.T) {
	for _, defect := range []string{"check", "index"} {
		t.Run(defect, func(t *testing.T) {
			ctx := t.Context()
			f := newInstanceFixture(t)
			const key = "synthetic_index_key_1234"
			_, err := f.db.ExecContext(ctx, "CREATE TABLE integrity_fixture(value TEXT CHECK (length(value)>2)); CREATE INDEX integrity_fixture_value ON integrity_fixture(value); INSERT INTO integrity_fixture VALUES (?)", key)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "source.sqlite")
			if _, err := f.db.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
				t.Fatal(err)
			}
			var page, size int64
			open := func(mode string) *sql.DB {
				t.Helper()
				db, err := sql.Open("sqlite", "file:"+path+"?mode="+mode)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				return db
			}
			src := open("rw")
			if defect == "check" {
				// These pragmas apply to this connection only.
				conn, err := src.Conn(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_, err = conn.ExecContext(ctx, "PRAGMA ignore_check_constraints=ON; INSERT INTO integrity_fixture VALUES ('x'); PRAGMA ignore_check_constraints=OFF")
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
			} else {
				// Change only the private copy's one-entry index leaf. Read the
				// page number from that copy because VACUUM can renumber pages.
				if err := src.QueryRowContext(ctx, "SELECT rootpage FROM sqlite_schema WHERE name='integrity_fixture_value'").Scan(&page); err != nil {
					t.Fatal(err)
				}
				if err := src.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
					t.Fatal(err)
				}
				if err := src.Close(); err != nil {
					t.Fatal(err)
				}
				file, err := os.OpenFile(path, os.O_RDWR, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { file.Close() })
				buf := make([]byte, size)
				offset := (page - 1) * size
				if _, err := file.ReadAt(buf, offset); err != nil {
					t.Fatal(err)
				}
				if buf[0] != 0x0a || bytes.Count(buf, []byte(key)) != 1 {
					t.Fatal("expected a unique fixture key on an index leaf")
				}
				if _, err := file.WriteAt([]byte("X"), offset+int64(bytes.Index(buf, []byte(key)))); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				src = open("rw")
			}
			for _, conn := range []struct {
				mode string
				db   *sql.DB
			}{{"rw", src}, {"ro", open("ro")}} {
				for _, pragma := range []string{"quick_check", "integrity_check"} {
					var verdict string
					if err := conn.db.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&verdict); err != nil {
						t.Fatal(err)
					}
					t.Logf("mode=%s %s first result=%q", conn.mode, pragma, verdict)
					if conn.mode == "rw" && pragma == "integrity_check" && !strings.Contains(verdict, "integrity_fixture") {
						t.Fatalf("fixture defect unconfirmed: %s", verdict)
					}
				}
			}
			f.db = src
			created := f.create(t, true)
			assertBundledSQLiteDefect(t, created.Path, f.identity)
			v, err := backup.Verify(ctx, created.Path)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("create succeeded; verify valid=%t completeness_checked=%t reason=%q", v.Valid, v.CompletenessChecked, v.CompletenessSkipReason)
			if !v.Valid {
				t.Fatal("intact encrypted bundle checksum rejected")
			}
			ids := []age.Identity{f.identity}
			c, err := backup.CheckBundleContents(ctx, created.Path, ids, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("contents ok=%t proof=%d problems=%v", c.OK, c.ProofLevel, c.Problems)
			if c.OK || c.ProofLevel >= backup.ProofContents {
				t.Error("damaged database received contents proof")
			}
			d, err := backup.DrillInstance(ctx, backup.DrillOptions{BundlePath: created.Path, Identities: ids, TempRoot: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("drill result=%s failures=%v", d.Result, d.Failures())
			if d.Result == backup.RestoreResultOK {
				t.Error("drill certified a damaged database")
			}
		})
	}
}

// Verify the defect in the actual sealed bytes, independently of the source DB
// and the product's proof result. A backup that repairs the image must not
// count as a reproduction of a false positive.
func assertBundledSQLiteDefect(t *testing.T, bundle string, identity age.Identity) {
	t.Helper()
	file, err := os.Open(bundle)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	_, payload, closeBundle, err := backup.ReadBundleStream(file, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer closeBundle()
	plain, err := backup.DecryptStream(payload, identity)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := backup.NewTarZstReader(plain, t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for {
		entry, err := tr.Next()
		if err != nil {
			t.Fatalf("locate database in fixture bundle: %v", err)
		}
		if entry.Name != "instance/db.sqlite" {
			continue
		}
		snapshot, err := os.CreateTemp(t.TempDir(), "snapshot-*.sqlite")
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.CopyN(snapshot, tr, entry.Size)
		closeErr := snapshot.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("extract fixture database: %v, close: %v", err, closeErr)
		}
		db, err := sql.Open("sqlite", "file:"+snapshot.Name()+"?mode=rw")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var verdict string
		if err := db.QueryRowContext(t.Context(), "PRAGMA integrity_check").Scan(&verdict); err != nil {
			t.Fatal(err)
		}
		t.Logf("bundled SQLite integrity_check=%q", verdict)
		if !strings.Contains(verdict, "integrity_fixture") {
			t.Fatalf("bundle does not reproduce fixture defect: %s", verdict)
		}
		return
	}
}
