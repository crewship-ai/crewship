package pipeline

import (
	"strings"
	"testing"
	"time"
)

func TestLegacyVersionWritersDoNotReusePublicationAuthority(t *testing.T) {
	for _, writer := range []string{"standalone", "transactional"} {
		t.Run(writer, func(t *testing.T) {
			db := openVersioningTestDB(t)
			t.Cleanup(func() { _ = db.Close() })
			seedPipeline(t, db, "published", "published")
			const raw = `{"dsl_version":"1.0","name":"same-content","steps":[]}`
			fingerprint := strings.Repeat("a", 64)
			hash := definitionHash(raw)
			if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,authority_fingerprint) VALUES('bounded','published',1,?,?,'user','publisher',?)`, raw, hash, fingerprint); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE pipelines SET head_version=1,definition_json=?,definition_hash=? WHERE id='published'`, raw, hash); err != nil {
				t.Fatal(err)
			}
			s := NewStore(db)
			for range 2 {
				// These are the two existing trusted storage writers, not an
				// assertion of paid-test or request authorization coverage.
				if writer == "standalone" {
					if _, err := s.SaveVersion(t.Context(), SaveVersionInput{PipelineID: "published", DefinitionJSON: raw, AuthorType: "user", AuthorID: "trusted-author"}); err != nil {
						t.Fatal(err)
					}
				} else {
					tx, err := db.BeginTx(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					input := SaveInput{DefinitionJSON: raw, Author: AuthorMeta{Via: AuthoredViaUser, UserID: "trusted-author"}}
					if err := s.saveVersionTx(t.Context(), tx, "published", input, hash, time.Now()); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
					if err := tx.Commit(); err != nil {
						t.Fatal(err)
					}
				}
				var version int
				var selected, original string
				if err := db.QueryRow(`SELECT p.head_version,v.authority_fingerprint FROM pipelines p JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version WHERE p.id='published'`).Scan(&version, &selected); err != nil {
					t.Fatal(err)
				}
				if err := db.QueryRow(`SELECT authority_fingerprint FROM pipeline_versions WHERE id='bounded'`).Scan(&original); err != nil {
					t.Fatal(err)
				}
				if version != 2 || selected != "legacy-trusted:v1" || original != fingerprint {
					t.Fatalf("trusted writer reused or changed bounded identity: version=%d selected=%s original=%s", version, selected, original)
				}
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_versions WHERE pipeline_id='published'`).Scan(&count); err != nil || count != 2 {
				t.Fatalf("same-authority dedup lost: count=%d err=%v", count, err)
			}
		})
	}
}
