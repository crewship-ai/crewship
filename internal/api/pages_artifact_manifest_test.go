package api

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestArtifactManifestExactSingleFileAndVerifiedLegacy(t *testing.T) {
	data := []byte("<h1>Žluťoučký</h1><script>untrusted()</script>")
	manifest, err := newArtifactManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 1 || manifest.RuntimeProfile != "static-html-v1" || manifest.Entrypoint != "index.html" || len(manifest.Files) != 1 || manifest.Files[0].MIME != artifactHTMLMIME || manifest.Files[0].SizeBytes != int64(len(data)) || manifest.Files[0].SHA256 != attachmentDigest(data) || string(manifest.Source) != "null" {
		t.Fatalf("manifest=%+v", manifest)
	}
	encoded, err := artifactManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		stored sql.NullString
		status string
	}{{sql.NullString{}, "legacy_derived"}, {sql.NullString{String: encoded, Valid: true}, "recorded"}} {
		got, status, err := resolveArtifactManifest(tc.stored, data, attachmentDigest(data))
		if err != nil || status != tc.status || got.Files[0].SizeBytes != int64(len(data)) {
			t.Fatalf("status=%q manifest=%+v err=%v", status, got, err)
		}
	}
	// A legacy label is not an integrity bypass.
	if _, _, err := resolveArtifactManifest(sql.NullString{}, data, strings.Repeat("0", 64)); err == nil {
		t.Fatal("legacy digest mismatch accepted")
	}
}

func TestArtifactManifestRejectsUnsupportedOrAmbiguousStoredContent(t *testing.T) {
	data := []byte("<h1>Build</h1>")
	manifest, err := newArtifactManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := artifactManifestJSON(manifest)
	for _, raw := range []string{
		"", "null", encoded + "{}", encoded + " ",
		strings.Replace(encoded, "static-html-v1", "interactive-html-v1", 1),
		strings.Replace(encoded, `"format_version":1`, `"format_version":2`, 1),
		strings.Replace(encoded, `"format_version":1`, `"format_version":2,"format_version":1`, 1),
		strings.Replace(encoded, "index.html", "../index.html", 1),
		strings.Replace(encoded, artifactHTMLMIME, "image/svg+xml", 1),
		strings.Replace(encoded, `"size_bytes":14`, `"size_bytes":1`, 1),
		strings.Replace(encoded, `"source":null`, `"source":{}`, 1),
		strings.Replace(encoded, `"source":null`, `"source":null,"unknown":true`, 1),
	} {
		t.Run(raw, func(t *testing.T) {
			if _, _, err := resolveArtifactManifest(sql.NullString{String: raw, Valid: true}, data, attachmentDigest(data)); err == nil {
				t.Fatalf("accepted %q", raw)
			}
		})
	}
	if _, _, err := resolveArtifactManifest(sql.NullString{String: encoded, Valid: true}, []byte("changed"), attachmentDigest(data)); err == nil {
		t.Fatal("manifest accepted different bytes")
	}
}

func TestArtifactFormatRequestOnlySupportsExplicitStaticV1(t *testing.T) {
	for _, raw := range []string{`{}`, `{"runtime_profile":"static-html-v1"}`, `{"format_version":1}`, `{"runtime_profile":"static-html-v1","format_version":1}`} {
		var req artifactFormatRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		if err := normalizeArtifactFormat(req); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"runtime_profile":null}`, `{"runtime_profile":""}`, `{"runtime_profile":"interactive-html-v1"}`, `{"runtime_profile":1}`, `{"format_version":null}`, `{"format_version":0}`, `{"format_version":2}`, `{"format_version":1.0}`, `{"format_version":"1"}`} {
		var req artifactFormatRequest
		if err := json.Unmarshal([]byte(raw), &req); err != nil {
			t.Fatal(err)
		}
		if err := normalizeArtifactFormat(req); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestArtifactManifestRejectsInvalidUTF8EmptyAndOversize(t *testing.T) {
	for _, data := range [][]byte{nil, {0xff}, []byte(strings.Repeat("x", maxArtifactHTMLBytes+1))} {
		if _, err := newArtifactManifest(data); err == nil {
			t.Fatalf("accepted %d-byte invalid content", len(data))
		}
	}
}

func TestArtifactManifestSchemaPreservesLegacyAndImmutableRecordedValues(t *testing.T) {
	h, _, _, _, user, page := artifactFixture(t)
	data := []byte("<h1>Build</h1>")
	manifest, err := newArtifactManifest(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := artifactManifestJSON(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id     string
		stored any
	}{{"legacy-manifest", nil}, {"recorded-manifest", encoded}} {
		revision := int64(1)
		if tc.id == "recorded-manifest" {
			revision = 2
		}
		execOrFatal(t, h.db, `INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,author_user_id,mutation_id,request_digest,created_at,manifest_json) VALUES(?,?,?,?,?,?,?,'2026-10-13T00:00:00.000Z',?)`, tc.id, page, revision, attachmentDigest(data), user, tc.id, tc.id, tc.stored)
		if _, err := h.db.Exec(`UPDATE page_artifact_revisions SET manifest_json=? WHERE id=?`, "{}", tc.id); err == nil {
			t.Fatalf("manifest mutation accepted: %s", tc.id)
		}
	}
	var legacy sql.NullString
	if err := h.db.QueryRow(`SELECT manifest_json FROM page_artifact_revisions WHERE id='legacy-manifest'`).Scan(&legacy); err != nil || legacy.Valid {
		t.Fatalf("legacy size/manifest invented: %v %v", legacy, err)
	}
	if _, err := h.db.Exec(`INSERT INTO page_artifact_revisions(id,page_id,revision,sha256,mutation_id,request_digest,created_at,manifest_json) VALUES('invalid-manifest',?,3,?,'invalid','invalid','2026-10-13T00:00:00.000Z','{')`, page, attachmentDigest(data)); err == nil {
		t.Fatal("invalid JSON manifest stored")
	}
}
