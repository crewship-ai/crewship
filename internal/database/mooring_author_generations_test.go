package database

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func seedAuthorGenerationCanonicalFixture(t *testing.T, tx *sql.Tx) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('generation-ws','Generations','generations'),('generation-other','Other','generation-other')`,
		`INSERT INTO users(id,email) VALUES('generation-alice','alice@synthetic.invalid'),('generation-bob','bob@synthetic.invalid'),('generation-carol','carol@synthetic.invalid')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('generation-alice-member','generation-ws','generation-alice','OWNER'),('generation-bob-member','generation-ws','generation-bob','MEMBER'),('generation-carol-member','generation-ws','generation-carol','ADMIN')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('generation-crew','generation-ws','Crew','crew'),('generation-crew-two','generation-ws','Other crew','crew-two')`,
		`INSERT INTO crew_members(id,crew_id,user_id) VALUES('generation-bob-crew','generation-crew','generation-bob'),('generation-carol-crew','generation-crew','generation-carol')`,
		`INSERT INTO agents(id,workspace_id,crew_id,name,slug) VALUES('generation-agent','generation-ws','generation-crew','Agent','agent')`,
		`INSERT INTO page_folders(id,workspace_id,slug,name,owner_crew_id,created_at,updated_at) VALUES('generation-folder','generation-ws','folder','Folder','generation-crew','2026-10-24','2026-10-24'),('generation-folder-two','generation-ws','folder-two','Folder two','generation-crew-two','2026-10-24','2026-10-24')`,
		`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,folder_id) VALUES('generation-page','generation-ws','page','Page','generation-alice','{}','generation-folder')`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-page','user','generation-bob','write','generation-alice'),('generation-page','agent','generation-agent','write','generation-alice')`,
		`INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_write,set_by_user_id,set_at) VALUES('generation-folder','user','generation-bob',1,'generation-carol','2026-10-24')`,
		`INSERT INTO workspace_artifact_creation_policy(workspace_id,enabled,version,updated_at) VALUES('generation-ws',1,1,'2026-10-24')`,
		`INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES('generation-ws','generation-crew')`,
	} {
		if _, err := tx.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// Explicit tracking stands in only for the future trusted first-admission
// writer. Canonical mutation/revalidation must never allocate an absent key.
func seedAuthorGenerationFixture(t *testing.T, tx *sql.Tx) {
	seedAuthorGenerationCanonicalFixture(t, tx)
	for _, k := range []authorGenerationKey{{"user", "alice"}, {"user", "bob"}, {"user", "carol"}, {"agent", "agent"}, {"page", "page"}, {"app", "crew"}} {
		if _, err := tx.Exec(`INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES('generation-ws',?,?)`, k.kind, "generation-"+k.id); err != nil {
			t.Fatal(err)
		}
	}
}

func readAuthorGeneration(t *testing.T, tx *sql.Tx, kind, id string) int64 {
	t.Helper()
	var n int64
	if err := tx.QueryRow(`SELECT revision FROM mooring_authority_generations WHERE workspace_id='generation-ws' AND scope_kind=? AND scope_id=?`, kind, "generation-"+id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMooringAuthorGenerationsSeparateRecipientFromDeliveryEpoch(t *testing.T) {
	db := openMigratedTestDB(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedAuthorGenerationFixture(t, tx)
	before := map[string]int64{}
	for _, key := range []string{"alice", "bob", "carol"} {
		before[key] = readAuthorGeneration(t, tx, "user", key)
	}
	before["agent"] = readAuthorGeneration(t, tx, "agent", "agent")
	before["page"] = readAuthorGeneration(t, tx, "page", "page")
	var deliveryBefore, deliveryAfter int64
	if err = tx.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='generation-ws'`).Scan(&deliveryBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id='generation-page' AND subject_type='user' AND subject_id='generation-bob'`); err != nil {
		t.Fatal(err)
	}
	if readAuthorGeneration(t, tx, "user", "bob") <= before["bob"] {
		t.Fatal("revoked recipient generation unchanged")
	}
	for _, key := range []string{"alice", "carol"} {
		if got := readAuthorGeneration(t, tx, "user", key); got != before[key] {
			t.Fatalf("unrelated user %s invalidated: %d -> %d", key, before[key], got)
		}
	}
	if readAuthorGeneration(t, tx, "agent", "agent") != before["agent"] || readAuthorGeneration(t, tx, "page", "page") != before["page"] {
		t.Fatal("unrelated author/Page generation invalidated")
	}
	if err = tx.QueryRow(`SELECT authority_revision FROM workspaces WHERE id='generation-ws'`).Scan(&deliveryAfter); err != nil || deliveryAfter <= deliveryBefore {
		t.Fatal("delivery epoch was not conservatively invalidated", err)
	}
	if _, err = tx.Exec(`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-page','user','generation-bob','write','generation-alice')`); err != nil {
		t.Fatal(err)
	}
	if readAuthorGeneration(t, tx, "user", "bob") <= before["bob"] {
		t.Fatal("grant ABA restored original generation")
	}
}

func TestMooringAuthorGenerationIdentityAndOverflowAreAtomic(t *testing.T) {
	db := openMigratedTestDB(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedAuthorGenerationFixture(t, tx)
	where := ` WHERE workspace_id='generation-ws' AND scope_kind='user' AND scope_id='generation-bob'`
	if _, err = tx.Exec(`UPDATE mooring_authority_generations SET revision=revision+1` + where); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE mooring_authority_generations SET revision=1` + where,
		`UPDATE mooring_authority_generations SET revision=1.5` + where,
		`UPDATE mooring_authority_generations SET scope_id='replacement'` + where,
		`DELETE FROM mooring_authority_generations` + where,
		`INSERT OR REPLACE INTO mooring_authority_generations(workspace_id,scope_kind,scope_id,revision) VALUES('generation-ws','user','generation-bob',1)`,
	} {
		if _, err = tx.Exec(q); err == nil {
			t.Fatalf("generation reset accepted: %s", q)
		}
	}
	if _, err = tx.Exec(`UPDATE mooring_authority_generations SET revision=9223372036854775807` + where); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = tx.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id='generation-ws'`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`DELETE FROM page_grants WHERE page_id='generation-page' AND subject_type='user' AND subject_id='generation-bob'`); err == nil {
		t.Fatal("counter overflow accepted canonical revocation")
	}
	if err = tx.QueryRow(`SELECT CAST(authority_revision AS TEXT) FROM workspaces WHERE id='generation-ws'`).Scan(&after); err != nil || before != after {
		t.Fatal("overflow did not roll back earlier delivery invalidation", err)
	}
	var count int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM page_grants WHERE page_id='generation-page' AND subject_type='user' AND subject_id='generation-bob'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("overflow lost canonical grant", err)
	}
}

type authorGenerationKey struct{ kind, id string }

func TestMooringAuthorGenerationCanonicalMutationMatrix(t *testing.T) {
	db := openMigratedTestDB(t)
	user := func(id string) authorGenerationKey { return authorGenerationKey{"user", id} }
	agent := authorGenerationKey{"agent", "agent"}
	page := authorGenerationKey{"page", "page"}
	app := authorGenerationKey{"app", "crew"}
	keys := []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, page, app}
	type vector struct {
		name            string
		prepare, mutate []string
		changed, stable []authorGenerationKey
	}
	vectors := []vector{
		{name: "grant recipient old and new", mutate: []string{`UPDATE page_grants SET subject_id='generation-carol' WHERE subject_type='user'`}, changed: []authorGenerationKey{user("bob"), user("carol")}, stable: []authorGenerationKey{user("alice"), agent, page, app}},
		{name: "grant issuer replacement", mutate: []string{`UPDATE page_grants SET granted_by_user_id='generation-carol' WHERE subject_type='user'`}, changed: []authorGenerationKey{user("bob")}, stable: []authorGenerationKey{user("alice"), user("carol"), agent, page, app}},
		{name: "workspace grant names humans only", mutate: []string{`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-page','workspace','','read','generation-alice')`}, changed: []authorGenerationKey{user("alice"), user("bob"), user("carol")}, stable: []authorGenerationKey{agent, page, app}},
		{name: "folder recipient ABA", mutate: []string{`DELETE FROM page_folder_acl WHERE folder_id='generation-folder'`, `INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_write,set_by_user_id,set_at) VALUES('generation-folder','user','generation-bob',1,'generation-carol','2026-10-24')`}, changed: []authorGenerationKey{user("bob")}, stable: []authorGenerationKey{user("alice"), user("carol"), agent, page, app}},
		{name: "folder setter is audit only", mutate: []string{`UPDATE page_folder_acl SET set_by_user_id='generation-alice',set_at='2026-10-25'`}, stable: keys},
		{name: "folder crew recipients", mutate: []string{`INSERT INTO page_folder_acl(folder_id,subject_type,subject_id,can_write,set_by_user_id,set_at) VALUES('generation-folder','crew','generation-crew',1,'generation-alice','2026-10-24')`}, changed: []authorGenerationKey{user("bob"), user("carol")}, stable: []authorGenerationKey{user("alice"), agent, page, app}},
		{name: "issuer suspended restored", mutate: []string{`UPDATE users SET suspended_at='2026-10-24' WHERE id='generation-alice'`, `UPDATE users SET suspended_at=NULL WHERE id='generation-alice'`}, changed: []authorGenerationKey{user("alice"), user("bob"), agent}, stable: []authorGenerationKey{user("carol"), page, app}},
		{name: "issuer role ABA", prepare: []string{`UPDATE page_grants SET granted_by_user_id='generation-carol' WHERE subject_type='agent'`}, mutate: []string{`UPDATE workspace_members SET role='MEMBER' WHERE user_id='generation-carol'`, `UPDATE workspace_members SET role='ADMIN' WHERE user_id='generation-carol'`}, changed: []authorGenerationKey{user("carol"), agent}, stable: []authorGenerationKey{user("alice"), user("bob"), page, app}},
		{name: "issuer global restricted ceiling ABA", prepare: []string{`UPDATE workspace_members SET role='MEMBER' WHERE user_id='generation-carol'`, `UPDATE pages SET owner_user_id=NULL,owner_crew_id='generation-crew' WHERE id='generation-page'`, `UPDATE page_grants SET granted_by_user_id='generation-carol'`}, mutate: []string{`INSERT INTO workspace_members(id,workspace_id,user_id,role,access_mode) VALUES('generation-ceiling','generation-other','generation-carol','MEMBER','restricted')`, `DELETE FROM workspace_members WHERE id='generation-ceiling'`}, changed: []authorGenerationKey{user("carol"), user("bob"), agent}, stable: []authorGenerationKey{user("alice"), page, app}},
		{name: "crew membership ABA", mutate: []string{`DELETE FROM crew_members WHERE id='generation-bob-crew'`, `INSERT INTO crew_members(id,crew_id,user_id) VALUES('generation-bob-crew','generation-crew','generation-bob')`}, changed: []authorGenerationKey{user("bob")}, stable: []authorGenerationKey{user("alice"), user("carol"), agent, page, app}},
		{name: "agent deletion state ABA", mutate: []string{`UPDATE agents SET deleted_at='2026-10-24',expired_at='2026-10-24' WHERE id='generation-agent'`, `UPDATE agents SET deleted_at=NULL,expired_at=NULL WHERE id='generation-agent'`}, changed: []authorGenerationKey{agent}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), page, app}},
		{name: "agent crew binding ABA", mutate: []string{`UPDATE agents SET crew_id='generation-crew-two' WHERE id='generation-agent'`, `UPDATE agents SET crew_id='generation-crew' WHERE id='generation-agent'`}, changed: []authorGenerationKey{agent}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), page, app}},
		{name: "page audience ABA", mutate: []string{`UPDATE pages SET private_audience=1 WHERE id='generation-page'`, `UPDATE pages SET private_audience=0 WHERE id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "page owner ABA", mutate: []string{`UPDATE pages SET owner_user_id='generation-carol' WHERE id='generation-page'`, `UPDATE pages SET owner_user_id='generation-alice' WHERE id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "page folder ABA", mutate: []string{`UPDATE pages SET folder_id='generation-folder-two' WHERE id='generation-page'`, `UPDATE pages SET folder_id='generation-folder' WHERE id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "folder owner crew ABA", mutate: []string{`UPDATE page_folders SET owner_crew_id='generation-crew-two' WHERE id='generation-folder'`, `UPDATE page_folders SET owner_crew_id='generation-crew' WHERE id='generation-folder'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "app policy ABA", mutate: []string{`UPDATE workspace_artifact_creation_policy SET enabled=0 WHERE workspace_id='generation-ws'`, `UPDATE workspace_artifact_creation_policy SET enabled=1 WHERE workspace_id='generation-ws'`}, changed: []authorGenerationKey{app}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, page}},
		{name: "app crew grant ABA", mutate: []string{`DELETE FROM workspace_artifact_creation_crews WHERE crew_id='generation-crew'`, `INSERT INTO workspace_artifact_creation_crews(workspace_id,crew_id) VALUES('generation-ws','generation-crew')`}, changed: []authorGenerationKey{app}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, page}},
		{name: "workspace deletion state ABA", mutate: []string{`UPDATE workspaces SET deleted_at='2026-10-24' WHERE id='generation-ws'`, `UPDATE workspaces SET deleted_at=NULL WHERE id='generation-ws'`}, changed: keys},
		{name: "collaboration content incarnation ABA", prepare: []string{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('generation-page','generation-ws','wordcraft','0','e30=')`}, mutate: []string{`DELETE FROM mooring_collab_documents WHERE page_id='generation-page'`, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('generation-page','generation-ws','wordcraft','0','e30=')`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "ordinary collaboration head writes preserve author generation", prepare: []string{`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES('generation-page','generation-ws','wordcraft','0','e30=')`}, mutate: []string{`UPDATE mooring_collab_documents SET revision='1',checkpoint_base64='eyJ0ZXN0IjoxfQ==' WHERE page_id='generation-page'`}, stable: keys},
		{name: "document presence ABA", prepare: []string{`UPDATE pages SET private_audience=1,folder_id=NULL WHERE id='generation-page'`}, mutate: []string{`INSERT INTO documents(workspace_id,page_id,schema_version,head_rev,snapshot,projection) VALUES('generation-ws','generation-page',1,1,x'00',x'00')`, `DELETE FROM documents WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "slides presence ABA", prepare: []string{`UPDATE pages SET private_audience=1,folder_id=NULL WHERE id='generation-page'`}, mutate: []string{`INSERT INTO slides_heads(workspace_id,page_id,schema_version,head_rev,order_version,canonical_json,content_hash) VALUES('generation-ws','generation-page',1,1,1,x'7b7d','synthetic')`, `DELETE FROM slides_heads WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "workbook presence ABA", prepare: []string{`UPDATE pages SET private_audience=1,folder_id=NULL WHERE id='generation-page'`}, mutate: []string{`INSERT INTO gridcraft_heads(workspace_id,page_id,schema_version,head_rev,generation,snapshot) VALUES('generation-ws','generation-page',1,1,1,x'00')`, `DELETE FROM gridcraft_heads WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "project draft presence ABA", mutate: []string{`INSERT INTO page_project_drafts(page_id,source_digest,revision,spec_json,updated_at) VALUES('generation-page','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',1,'{}','2026-10-24')`, `DELETE FROM page_project_drafts WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "legacy native presence ABA", prepare: []string{`UPDATE pages SET private_audience=1,folder_id=NULL WHERE id='generation-page'`}, mutate: []string{`INSERT INTO mooring_documents(page_id,workspace_id,app_id,rev,snapshot) VALUES('generation-page','generation-ws','legacy',0,'{}')`, `DELETE FROM mooring_documents WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "ordinary legacy native payload preserves author generation", prepare: []string{`UPDATE pages SET private_audience=1,folder_id=NULL WHERE id='generation-page'`, `INSERT INTO mooring_documents(page_id,workspace_id,app_id,rev,snapshot) VALUES('generation-page','generation-ws','legacy',0,'{}')`}, mutate: []string{`UPDATE mooring_documents SET rev=1,snapshot='{"test":1}' WHERE page_id='generation-page'`}, stable: keys},
		{name: "panel crew policy ABA", prepare: []string{`INSERT INTO page_panels(id,page_id,panel_id,schema,owner_crew_id,producer_kind,producer_ref,sla_seconds) VALUES('generation-panel','generation-page','panel','metric.v1','generation-crew','script','fixture',60)`}, mutate: []string{`UPDATE page_panels SET owner_crew_id='generation-crew-two' WHERE id='generation-panel'`, `UPDATE page_panels SET owner_crew_id='generation-crew' WHERE id='generation-panel'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "publication withdrawn restored ABA", prepare: []string{`INSERT INTO page_project_builds(id,page_id,source_revision,source_digest,state,created_at) VALUES('generation-build','generation-page',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','ready','2026-10-24')`, `INSERT INTO page_project_publications(page_id,version,build_id,source_revision,source_digest,git_commit,artifact_digest,spec_json,checks_json,created_at) VALUES('generation-page',1,'generation-build',1,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','synthetic','bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb','{}','{}','2026-10-24')`, `INSERT INTO page_project_live(page_id,version) VALUES('generation-page',1)`}, mutate: []string{`UPDATE page_project_live SET published=0 WHERE page_id='generation-page'`, `UPDATE page_project_live SET published=1 WHERE page_id='generation-page'`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("bob"), user("carol"), agent, app}},
		{name: "broad delivery epoch alone is not author revocation", mutate: []string{`UPDATE workspaces SET authority_revision=authority_revision+1 WHERE id='generation-ws'`}, stable: keys},
		{name: "user delete recreate tombstone", mutate: []string{`DELETE FROM users WHERE id='generation-bob'`, `INSERT INTO users(id,email) VALUES('generation-bob','bob@synthetic.invalid')`, `INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('generation-bob-member','generation-ws','generation-bob','MEMBER')`}, changed: []authorGenerationKey{user("bob")}, stable: []authorGenerationKey{user("alice"), user("carol"), agent, page, app}},
		{name: "page delete recreate tombstone", mutate: []string{`DELETE FROM pages WHERE id='generation-page'`, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,folder_id) VALUES('generation-page','generation-ws','page','Page','generation-alice','{}','generation-folder')`}, changed: []authorGenerationKey{page}, stable: []authorGenerationKey{user("alice"), user("carol"), app}},
	}
	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			seedAuthorGenerationFixture(t, tx)
			for _, q := range v.prepare {
				if _, err = tx.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
			before := map[authorGenerationKey]int64{}
			for _, k := range keys {
				before[k] = readAuthorGeneration(t, tx, k.kind, k.id)
			}
			for _, q := range v.mutate {
				if _, err = tx.Exec(q); err != nil {
					t.Fatalf("mutation: %v", err)
				}
			}
			for _, k := range v.changed {
				if got := readAuthorGeneration(t, tx, k.kind, k.id); got <= before[k] {
					t.Errorf("%v did not advance: %d -> %d", k, before[k], got)
				}
			}
			for _, k := range v.stable {
				if got := readAuthorGeneration(t, tx, k.kind, k.id); got != before[k] {
					t.Errorf("unrelated %v changed: %d -> %d", k, before[k], got)
				}
			}
		})
	}
}

func TestMooringAuthorGenerationWriterRollbackRetainsExactCounters(t *testing.T) {
	db := openMigratedTestDB(t)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedAuthorGenerationFixture(t, tx)
	snapshot := func() map[string]int64 {
		rows, e := tx.Query(`SELECT scope_kind,scope_id,revision FROM mooring_authority_generations WHERE workspace_id='generation-ws'`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		values := map[string]int64{}
		for rows.Next() {
			var kind, id string
			var n int64
			if e = rows.Scan(&kind, &id, &n); e != nil {
				t.Fatal(e)
			}
			values[fmt.Sprint(kind, ":", id)] = n
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return values
	}
	before := snapshot()
	if _, err = tx.Exec(`SAVEPOINT generation_rollback; DELETE FROM page_grants WHERE subject_type='agent'; ROLLBACK TO generation_rollback; RELEASE generation_rollback`); err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); !reflect.DeepEqual(before, after) {
		t.Fatal("rolled back canonical write retained generation effects")
	}
	// Preserve exact signed integer text beyond Javascript's safe-integer range.
	if _, err = tx.Exec(`UPDATE mooring_authority_generations SET revision=9007199254740993 WHERE workspace_id='generation-ws' AND scope_kind='agent'`); err != nil {
		t.Fatal(err)
	}
	var exact string
	if err = tx.QueryRow(`SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id='generation-ws' AND scope_kind='agent'`).Scan(&exact); err != nil || exact != "9007199254740993" {
		t.Fatal("generation lost integer precision", err)
	}
}

func TestMooringAuthorGenerationUntrackedPolicyChangesAllocateNothing(t *testing.T) {
	db := openMigratedTestDB(t)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	seedAuthorGenerationCanonicalFixture(t, tx)
	for _, q := range []string{
		`DELETE FROM page_grants WHERE subject_type='user'`,
		`INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-page','user','generation-bob','write','generation-alice')`,
		`UPDATE users SET suspended_at='2026-10-24' WHERE id='generation-alice'`,
		`UPDATE users SET suspended_at=NULL WHERE id='generation-alice'`,
		`UPDATE pages SET private_audience=1 WHERE id='generation-page'`,
		`UPDATE pages SET private_audience=0 WHERE id='generation-page'`,
		`UPDATE workspace_artifact_creation_policy SET enabled=0 WHERE workspace_id='generation-ws'`,
	} {
		if _, err = tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM mooring_authority_generations`).Scan(&n); err != nil || n != 0 {
		t.Fatal("inactive/untracked canonical mutations allocated generation state", n, err)
	}
}

func TestMooringAuthorGenerationFirstTrackingSerializesWithCanonicalGrantABA(t *testing.T) {
	db := openMigratedTestDB(t)
	db.SetMaxOpenConns(2)
	seed, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	seedAuthorGenerationCanonicalFixture(t, seed)
	if err = seed.Commit(); err != nil {
		t.Fatal(err)
	}
	capture, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Rollback()
	// This is a SQL tracking-order test, not an authentication implementation.
	// The future fresh-human admission must perform its actual canonical checks
	// and complete every baseline key under this same already-acquired writer.
	if _, err = capture.Exec(`UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	changed := make(chan error, 1)
	go func() {
		close(started)
		_, e := db.ExecContext(t.Context(), `DELETE FROM page_grants WHERE subject_type='user'; INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('generation-page','user','generation-bob','write','generation-alice')`)
		changed <- e
	}()
	<-started
	select {
	case e := <-changed:
		t.Fatal("canonical mutation passed the held capture writer", e)
	case <-time.After(20 * time.Millisecond):
	}
	if _, err = capture.Exec(`INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) VALUES('generation-ws','user','generation-bob')`); err != nil {
		t.Fatal(err)
	}
	original := readAuthorGeneration(t, capture, "user", "bob")
	if original != 1 {
		t.Fatal("fresh tracking did not start at exact baseline1")
	}
	if err = capture.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-changed; err != nil {
		t.Fatal(err)
	}
	var current int64
	if err = db.QueryRow(`SELECT revision FROM mooring_authority_generations WHERE workspace_id='generation-ws' AND scope_kind='user' AND scope_id='generation-bob'`).Scan(&current); err != nil || current <= original {
		t.Fatal("raced grant ABA revived original captured generation", original, current, err)
	}
	var count int
	if err = db.QueryRow(`SELECT COUNT(*) FROM mooring_authority_generations`).Scan(&count); err != nil || count != 1 {
		t.Fatal("mutation installed an untracked key", count, err)
	}
}
