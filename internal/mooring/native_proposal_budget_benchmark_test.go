package mooring

import (
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func BenchmarkNativeProposalRetainedBytes(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE craft_native_proposals(workspace_id TEXT,page_id TEXT,content BLOB,summary TEXT,author_actor_id TEXT,request_id TEXT,id TEXT,engine_pin TEXT,content_hash TEXT,created_at TEXT)`); err != nil {
		b.Fatal(err)
	}
	raw := []byte(strings.Repeat("x", 4<<20))
	for range 15 {
		if _, err := db.Exec(`INSERT INTO craft_native_proposals VALUES('ws','page',?,'','agent','request','id','pin','hash','time')`, raw); err != nil {
			b.Fatal(err)
		}
	}
	const expression = "CASE WHEN typeof(content)='blob' THEN length(content) ELSE length(CAST(content AS BLOB)) END"
	baseline := strings.Replace(nativeProposalBytesQuery, expression, "length(CAST(content AS BLOB))", 1)
	if baseline == nativeProposalBytesQuery {
		b.Fatal("benchmark baseline no longer matches the production query")
	}
	for _, tc := range []struct{ name, query string }{{"cast-loads-payload", baseline}, {"blob-metadata", nativeProposalBytesQuery}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				var used int64
				if err := db.QueryRowContext(b.Context(), tc.query, "ws", "page").Scan(&used); err != nil || used < 15*int64(len(raw)) {
					b.Fatal(used, err)
				}
			}
		})
	}
}
