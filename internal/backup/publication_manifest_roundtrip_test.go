package backup_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPublicationManifestAndExplicitGrantsSurviveRestore(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedExecutionOriginHistory(t, source)
	// Deliberately opaque historical metadata: this is a backup contract test,
	// not a fabricated valid publication or execution admission.
	const manifest = `{"historical_workspace":"ws_e2e_1","metadata_only":true}`
	fingerprint := strings.Repeat("a", 64)
	if _, err := source.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id,chat_origin_id,authority_fingerprint,publication_authority_json)
SELECT 'manifest-version',pipeline_id,3,definition_json,definition_hash,author_type,author_id,chat_origin_id,?,? FROM pipeline_versions WHERE id='origin-version';
UPDATE pipelines SET head_version=3 WHERE id='origin-pipeline';
INSERT INTO users(id,email) VALUES('manifest-reader','manifest-reader@backup.test');
INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('manifest-member','ws_e2e_1','manifest-reader','VIEWER')`, fingerprint, manifest); err != nil {
		t.Fatal(err)
	}
	store := access.Store{DB: source}
	member, err := store.Membership(t.Context(), "manifest-reader", workspace)
	if err != nil {
		t.Fatal(err)
	}
	rights := []access.Right{}
	for _, operation := range []string{"view", "edit", "publish"} {
		rights = append(rights, access.Right{Kind: "routine", ID: "origin-pipeline", Operation: operation})
	}
	if _, err := store.Replace(t.Context(), "u_admin", "manifest-reader", workspace, "restricted", member, rights); err != nil {
		t.Fatal(err)
	}
	for _, fork := range []bool{false, true} {
		t.Run(fmt.Sprintf("fork=%t", fork), func(t *testing.T) {
			target := openMigratedDB(t)
			dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if fork {
				if err := backup.RemapIDs(t.Context(), target, dump); err != nil {
					t.Fatal(err)
				}
				dump.Tables["workspaces"][0]["slug"] = "manifest-fork"
			}
			if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
				t.Fatal(err)
			}
			var pipelineID, actualManifest, actualFingerprint string
			if err := target.QueryRow(`SELECT pipeline_id,publication_authority_json,authority_fingerprint FROM pipeline_versions WHERE version=3`).Scan(&pipelineID, &actualManifest, &actualFingerprint); err != nil {
				t.Fatal(err)
			}
			if actualManifest != manifest || actualFingerprint != fingerprint || (fork && pipelineID == "origin-pipeline") {
				t.Fatal("restore changed historical metadata or failed to remap its typed parent")
			}
			var count int
			if err := target.QueryRow(`SELECT count(*) FROM access_grants WHERE routine_id=? AND operation IN ('view','edit','publish')`, pipelineID).Scan(&count); err != nil || count != 3 {
				t.Fatalf("restore silently lost explicit grants: %d %v", count, err)
			}
			pipelineStore := pipeline.NewStore(target)
			p, err := pipelineStore.GetByID(t.Context(), pipelineID)
			if err != nil {
				t.Fatal(err)
			}
			if err := pipelineStore.CheckGenericRun(t.Context(), p, nil); !errors.Is(err, pipeline.ErrPublicationGenericRun) {
				t.Fatalf("restored metadata authorized generic execution: %v", err)
			}
			if _, err := target.Exec(`UPDATE pipeline_versions SET publication_authority_json='{}' WHERE version=3`); err == nil {
				t.Fatal("restored manifest became mutable")
			}
		})
	}
}
