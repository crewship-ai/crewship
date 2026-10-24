package api

import (
	"context"
	"database/sql"
	"errors"
	"math"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
)

var errCollaborationAuthorUnqualified = errors.New("original collaboration author generations unavailable")

// Original host-only generation metadata, not an ACL or a refreshed admission.
// The broad workspace delivery epoch is deliberately separate. Strings retain
// exact SQLite integer counters beyond JavaScript's safe numeric range.
type collaborationAuthorVector struct {
	workspace, user, page, agent, crew                     string
	userRevision, pageRevision, agentRevision, appRevision string
	schemaCookie                                           int64
}

func collaborationQualifiedSchema(ctx context.Context, tx *sql.Tx) (int64, error) {
	if tx == nil {
		return 0, errCollaborationAuthorUnqualified
	}
	var unsupported bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_schema WHERE name COLLATE NOCASE IN ('page_boards','page_artifacts') AND type IN ('table','view'))`).Scan(&unsupported); err != nil {
		return 0, err
	}
	if unsupported {
		return 0, errCollaborationAuthorUnqualified
	}
	var cookie int64
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&cookie); err != nil {
		return 0, err
	}
	if cookie <= 0 || cookie > math.MaxInt32 {
		return 0, errCollaborationAuthorUnqualified
	}
	return cookie, nil
}

func (v collaborationAuthorVector) keys() [][2]string {
	keys := [][2]string{{"user", v.user}, {"page", v.page}}
	if v.agent != "" {
		keys = append(keys, [2]string{"agent", v.agent}, [2]string{"app", v.crew})
	}
	return keys
}

func (v collaborationAuthorVector) validIdentity() bool {
	return mooring.ValidWireIdentifier(v.workspace) && mooring.ValidWireIdentifier(v.user) && mooring.ValidWireIdentifier(v.page) && ((v.agent == "" && v.crew == "") || (mooring.ValidWireIdentifier(v.agent) && mooring.ValidWireIdentifier(v.crew)))
}

func (v collaborationAuthorVector) matches(key mooring.SessionKey, user, agent, crew string) bool {
	return key.ApplicationID == "wordcraft" && v.workspace == key.WorkspaceID && v.page == key.ArtifactID && v.user == user && v.agent == agent && v.crew == crew && v.validIdentity()
}

// Only fresh actual-human admission/StartTask calls this after canonical allow,
// inside its already-acquired writer. Never call from reconnect, Attach, Check,
// decisions, serialized origins or restored rows. Publication follows COMMIT.
func captureCollaborationAuthorVector(ctx context.Context, tx *sql.Tx, key mooring.SessionKey, user, agent, crew string) (collaborationAuthorVector, error) {
	v := collaborationAuthorVector{workspace: key.WorkspaceID, user: user, page: key.ArtifactID, agent: agent, crew: crew}
	if key.ApplicationID != "wordcraft" || !v.validIdentity() {
		return collaborationAuthorVector{}, errCollaborationAuthorUnqualified
	}
	cookie, err := collaborationQualifiedSchema(ctx, tx)
	if err != nil {
		return collaborationAuthorVector{}, err
	}
	v.schemaCookie = cookie
	for _, k := range v.keys() {
		// The duplicate guard also rejects INSERT ON CONFLICT. The held writer
		// makes this conditional insertion and subsequent capture one order.
		if _, err = tx.ExecContext(ctx, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?)`, v.workspace, k[0], k[1], v.workspace, k[0], k[1]); err != nil {
			return collaborationAuthorVector{}, err
		}
	}
	return readCollaborationAuthorVector(ctx, tx, v)
}

// SELECT-only: absent/malformed tracking and schema drift are uncertainty, not
// canonical permission denial. Cookie equality is conservative qualification,
// not protection against privileged PRAGMA reset. Schema installers must run
// at startup with host-incarnation drain; general runtime DDL is unqualified.
func readCollaborationAuthorVector(ctx context.Context, tx *sql.Tx, original collaborationAuthorVector) (collaborationAuthorVector, error) {
	if !original.validIdentity() || original.schemaCookie <= 0 {
		return collaborationAuthorVector{}, errCollaborationAuthorUnqualified
	}
	cookie, err := collaborationQualifiedSchema(ctx, tx)
	if err != nil {
		return collaborationAuthorVector{}, err
	}
	if cookie != original.schemaCookie {
		return collaborationAuthorVector{}, errCollaborationAuthorUnqualified
	}
	v := original
	revisions := []*string{&v.userRevision, &v.pageRevision, &v.agentRevision, &v.appRevision}
	for i, k := range v.keys() {
		if err = tx.QueryRowContext(ctx, `SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?`, v.workspace, k[0], k[1]).Scan(revisions[i]); err != nil {
			return collaborationAuthorVector{}, err
		}
		if !chataudience.ValidUserAuthorityRevision(*revisions[i]) {
			return collaborationAuthorVector{}, errCollaborationAuthorUnqualified
		}
	}
	return v, nil
}

func collaborationAuthorVectorChanged(ctx context.Context, tx *sql.Tx, original collaborationAuthorVector) (bool, error) {
	if !chataudience.ValidUserAuthorityRevision(original.userRevision) || !chataudience.ValidUserAuthorityRevision(original.pageRevision) || (original.agent != "" && (!chataudience.ValidUserAuthorityRevision(original.agentRevision) || !chataudience.ValidUserAuthorityRevision(original.appRevision))) || (original.agent == "" && (original.agentRevision != "" || original.appRevision != "")) {
		return false, errCollaborationAuthorUnqualified
	}
	current, err := readCollaborationAuthorVector(ctx, tx, original)
	return err == nil && current != original, err
}
