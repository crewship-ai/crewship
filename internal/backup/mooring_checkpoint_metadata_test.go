package backup

import (
	"database/sql"
	"testing"
)

// Storage portability only: these synthetic metadata bytes are not an app or
// authorization qualification. Unknown legacy metadata must remain NULL.
func TestMooringCheckpointMetadataPortableExactAndLegacyUnknown(t *testing.T) {
	source := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, source, "checkpoint-metadata")
	user := "u_cov_checkpoint-metadata"
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('metadata-member',?,?,'OWNER')`, []any{workspace, user}},
		{`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('metadata-page',?,'metadata-page','Metadata',?,'{}')`, []any{workspace, user}},
		{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('metadata-page',?,'editor','1','MQ==')`, []any{workspace}},
		{`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES('metadata-page',?,'0','MA==',1)`, []any{workspace}},
	} {
		if _, err := source.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	const metadata = `{"assets":[{"asset_id":"asset-😀","digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000","size_bytes":"18446744073709551615"}],"format_version":"synthetic","versions":{"engine_version":"0.10.0","model_version":"0.2.0","protocol_version":"0.4.0"}}`
	if _, err := source.Exec(`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes,checkpoint_generation,checkpoint_metadata_json) VALUES('metadata-page',?,'1','MQ==',1,'18446744073709551615',?)`, workspace, metadata); err != nil {
		t.Fatal(err)
	}
	dump, err := DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalDump(dump)
	if err != nil {
		t.Fatal(err)
	}
	restoredDump, err := UnmarshalDump(raw)
	if err != nil {
		t.Fatal(err)
	}
	destination := openMigratedDBCov(t)
	if err = RestoreDump(t.Context(), destination, restoredDump); err != nil {
		t.Fatal(err)
	}
	var legacyMetadata, legacyGeneration sql.NullString
	if err = destination.QueryRow(`SELECT checkpoint_metadata_json,checkpoint_generation FROM mooring_collab_checkpoints WHERE page_id='metadata-page' AND revision='0'`).Scan(&legacyMetadata, &legacyGeneration); err != nil || legacyMetadata.Valid || legacyGeneration.Valid {
		t.Fatal("legacy interpretation inferred", legacyMetadata, legacyGeneration, err)
	}
	var actual, generation, state string
	if err = destination.QueryRow(`SELECT checkpoint_metadata_json,checkpoint_generation,checkpoint_base64 FROM mooring_collab_checkpoints WHERE page_id='metadata-page' AND revision='1'`).Scan(&actual, &generation, &state); err != nil || actual != metadata || generation != "18446744073709551615" || state != "MQ==" {
		t.Fatal("retained interpretation changed", actual, generation, state, err)
	}
}
