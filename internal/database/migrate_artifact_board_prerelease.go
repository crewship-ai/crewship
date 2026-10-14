package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// Temporary compatibility for the exact unreleased Artifact/Board schema. The
// managed reset command uses the installed application, while deployment first
// requires a healthy boot. Recognize the old final schema before any writes so
// the consolidated binary can boot, then the explicitly approved reset can run.
// This never repairs, deletes or renumbers ledger rows, accepts partial old
// schemas, or changes the generic newer-version guard.
var artifactBoardPrereleaseVersions = map[int]string{
	20261013132657: "page_artifacts",
	20261013151503: "page_artifact_comments",
	20261013151744: "page_artifact_run_delegations",
	20261013162044: "page_artifact_requests",
	20261013162114: "artifact_creation_authorizations",
	20261013162831: "artifact_workspace_writes",
	20261013163044: "artifact_principal_retention",
	20261013163244: "artifact_mutation_receipts",
	20261013181826: "page_boards",
	20261013183111: "page_comment_grants",
	20261013184743: "artifact_revision_source_chat",
	20261013190214: "board_agent_delegations",
	20261013190315: "board_files",
	20261013191348: "board_placement_undo",
	20261013193200: "board_comments",
	20261013193201: "board_requests",
	20261013193203: "drop_artifact_board_redundant_indexes",
	20261013193204: "board_element_permissions",
	20261013193205: "board_history_compaction",
	20261013202325: "artifact_agent_comments",
	20261013202326: "board_follow_latest",
	20261013202327: "board_mode_undo_source_pins",
	20261013203654: "board_request_previews",
	20261013205951: "artifact_comment_history",
	20261013211316: "page_artifact_copies",
	20261013212140: "artifact_revision_manifest",
	20261013213511: "artifact_creation_policy",
	20261013214510: "page_request_deadlines",
	20261013223010: "page_request_retries_notices",
	20261013223116: "artifact_request_updates",
	20261013231523: "board_comment_lifecycle",
	20261014051900: "board_file_retention",
	20261014054847: "board_file_retention_index",
}

var artifactBoardConsolidatedVersions = []int{20261014114745, 20261014114746, 20261014114747}

type artifactBoardSchemaObject struct {
	kind, name, table, definition string
}

func preflightArtifactBoardConsolidation(ctx context.Context, db *sql.DB) error {
	var feature []migration
	registered := map[int]bool{}
	registeredNames := map[int]string{}
	for _, m := range migrations {
		registered[m.version] = true
		registeredNames[m.version] = m.name
	}
	for _, version := range artifactBoardConsolidatedVersions {
		for _, m := range migrations {
			if m.version == version {
				feature = append(feature, m)
			}
		}
	}
	if len(feature) != 3 {
		return nil // Synthetic registry tests and binaries without this feature.
	}
	var ledger int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='_migrations'`).Scan(&ledger); err != nil {
		return fmt.Errorf("Artifact/Board schema preflight: %w", err)
	}
	applied := map[int]string{}
	legacy := 0
	var unknown []int
	if ledger != 0 {
		rows, err := db.QueryContext(ctx, `SELECT version,name FROM _migrations`)
		if err != nil {
			return fmt.Errorf("Artifact/Board schema preflight ledger: %w", err)
		}
		for rows.Next() {
			var version int
			var name string
			if err = rows.Scan(&version, &name); err != nil {
				rows.Close()
				return fmt.Errorf("Artifact/Board schema preflight ledger: %w", err)
			}
			// A renumbered ledger row can make a complete feature schema look
			// like an unfinished prefix. Preserve the existing repair-ledger
			// diagnosis before interpreting feature stages; never repair here.
			if expected, known := registeredNames[version]; known && expected != name {
				rows.Close()
				return fmt.Errorf("migration version %d collision: database has %q applied, code expects %q; refusing schema writes. Run `crewship db repair-ledger --dry-run` to inspect the ledger repair", version, name, expected)
			}
			applied[version] = name
			if want, ok := artifactBoardPrereleaseVersions[version]; ok {
				if name != want {
					rows.Close()
					return fmt.Errorf("Artifact/Board prerelease migration v%d is %q, expected %q; refusing schema writes", version, name, want)
				}
				legacy++
			} else if !registered[version] {
				unknown = append(unknown, version)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return fmt.Errorf("Artifact/Board schema preflight ledger: %w", err)
		}
	}
	stage := 0
	for i, m := range feature {
		if name, ok := applied[m.version]; ok {
			if name != m.name || i != stage {
				return fmt.Errorf("Artifact/Board consolidated migration ledger is inconsistent at v%d; refusing schema writes", m.version)
			}
			stage++
		}
	}
	if legacy != 0 && legacy != len(artifactBoardPrereleaseVersions) {
		return fmt.Errorf("Artifact/Board prerelease schema has %d of %d recognized migrations; refusing partial schema writes", legacy, len(artifactBoardPrereleaseVersions))
	}
	if legacy != 0 && len(unknown) != 0 {
		return fmt.Errorf("Artifact/Board prerelease ledger contains unrecognized migration v%d; refusing schema writes", unknown[0])
	}
	if legacy == 0 && stage == len(feature) {
		return nil // Normal consolidated installation; its ledger is authoritative.
	}
	actual, err := readArtifactBoardSchema(ctx, db)
	if err != nil {
		return err
	}
	if legacy == 0 && stage == 0 {
		for _, obj := range actual {
			if artifactBoardFeatureNamespace(obj.name) || artifactBoardFeatureNamespace(obj.table) {
				return fmt.Errorf("unrecognized Artifact/Board object %q without a compatible migration ledger; refusing schema writes", obj.name)
			}
		}
		return nil // Fresh database or shipped main, with no feature objects yet.
	}
	stages, err := expectedArtifactBoardSchemas(ctx, feature)
	if err != nil {
		return err
	}
	wantStage := stage
	if legacy != 0 {
		wantStage = len(feature) // Old33 must already be the complete final schema.
	}
	want, full := stages[wantStage], stages[len(feature)]
	featureTables := map[string]bool{}
	for _, obj := range full {
		if obj.kind == "table" {
			featureTables[obj.name] = true
		}
	}
	for key, expected := range want {
		if got, ok := actual[key]; !ok || got.definition != expected.definition || got.table != expected.table {
			return fmt.Errorf("Artifact/Board schema differs at %s; refusing schema writes", key)
		}
	}
	for key, obj := range actual {
		if featureTables[obj.table] || artifactBoardFeatureNamespace(obj.name) || artifactBoardFeatureNamespace(obj.table) {
			if _, ok := want[key]; !ok {
				return fmt.Errorf("unexpected Artifact/Board schema object %s; refusing schema writes", key)
			}
		}
	}
	return nil
}

func artifactBoardFeatureNamespace(name string) bool {
	name = strings.ToLower(name) // SQLite identifiers are case-insensitive.
	for _, prefix := range []string{"page_artifact", "page_board", "artifact_", "workspace_artifact", "board_", "idx_artifact", "idx_page_artifact", "idx_board", "idx_page_board", "page_grants_with_"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// Derive the recognized contract from the actual three embedded SQL programs,
// never a private capture or a hand-maintained parallel production schema. Only
// trigger attachment/FK target names and grant-copy columns need main stubs;
// these stubs are excluded from the comparison. No user data enters this DB.
func expectedArtifactBoardSchemas(ctx context.Context, feature []migration) ([]map[string]artifactBoardSchemaObject, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(ctx, `CREATE TABLE pages(id TEXT,workspace_id TEXT,owner_user_id TEXT,owner_crew_id TEXT);
CREATE TABLE page_panels(page_id TEXT); CREATE TABLE page_project_drafts(page_id TEXT); CREATE TABLE page_project_live(page_id TEXT);
CREATE TABLE users(id TEXT); CREATE TABLE agents(id TEXT); CREATE TABLE crews(id TEXT); CREATE TABLE chats(id TEXT); CREATE TABLE workspaces(id TEXT);
CREATE TABLE page_grants(page_id TEXT,subject_type TEXT,subject_id TEXT,level TEXT,panel_ids TEXT,granted_by_user_id TEXT,granted_at TEXT);`)
	if err != nil {
		return nil, fmt.Errorf("prepare Artifact/Board schema contract: %w", err)
	}
	baseline, err := readArtifactBoardSchema(ctx, db)
	if err != nil {
		return nil, err
	}
	stages := []map[string]artifactBoardSchemaObject{{}}
	for _, m := range feature {
		if _, err = db.ExecContext(ctx, m.sql); err != nil {
			return nil, fmt.Errorf("derive Artifact/Board schema contract from %s: %w", m.name, err)
		}
		objects, err := readArtifactBoardSchema(ctx, db)
		if err != nil {
			return nil, err
		}
		for key := range baseline {
			if key != "table page_grants" {
				delete(objects, key)
			}
		}
		stages = append(stages, objects)
	}
	return stages, nil
}

var artifactBoardDefinitionTokens = regexp.MustCompile(`(?s)'(?:[^']|'')*'|"(?:""|[^"])*"|--[^\n]*(?:\n|$)|/\*.*?\*/|[A-Za-z_][A-Za-z_0-9]*|[^\s]`)

func readArtifactBoardSchema(ctx context.Context, db *sql.DB) (map[string]artifactBoardSchemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite_%'`)
	if err != nil {
		return nil, fmt.Errorf("read Artifact/Board schema contract: %w", err)
	}
	defer rows.Close()
	objects := map[string]artifactBoardSchemaObject{}
	for rows.Next() {
		var obj artifactBoardSchemaObject
		if err = rows.Scan(&obj.kind, &obj.name, &obj.table, &obj.definition); err != nil {
			return nil, err
		}
		var tokens []string
		for _, token := range artifactBoardDefinitionTokens.FindAllString(obj.definition, -1) {
			if strings.HasPrefix(token, "--") || strings.HasPrefix(token, "/*") {
				continue
			}
			if strings.HasPrefix(token, `"`) {
				token = strings.ReplaceAll(token[1:len(token)-1], `""`, `"`)
			}
			if !strings.HasPrefix(token, "'") {
				token = strings.ToLower(token)
			}
			tokens = append(tokens, token)
		}
		obj.definition = strings.Join(tokens, " ")
		objects[obj.kind+" "+obj.name] = obj
	}
	return objects, rows.Err()
}
