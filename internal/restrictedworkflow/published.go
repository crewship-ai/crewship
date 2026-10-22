package restrictedworkflow

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// DefaultPublishedRunLimit bounds one Guest-published run from its scheduled
// start. The job deadline is immutable once accepted.
const DefaultPublishedRunLimit = 30 * time.Minute

// publishedRef pins the exact publication a frozen graph consumes. It is
// queue data selected on the host, never a request field or a capability.
type publishedRef struct {
	VersionID   string `json:"version_id"`
	Version     int    `json:"version"`
	Fingerprint string `json:"fingerprint"`
	Publisher   string `json:"publisher"`
	Membership  string `json:"membership"`
}

type rowQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// loadPublishedRoot reports published=false for a legacy recipe, which keeps
// its existing loader. A bounded manifest must match the live HEAD exactly.
func loadPublishedRoot(ctx context.Context, q rowQuery, workspace, id string) (*graphRecipe, publishedRef, access.PublicationAuthority, bool, error) {
	var ref publishedRef
	var manifest access.PublicationAuthority
	var physical, versionID, raw, fingerprint, canonical sql.NullString
	var version sql.NullInt64
	var physicalText, sameBody bool
	r := &graphRecipe{ID: id}
	err := q.QueryRowContext(ctx, `SELECT p.publication_authority_fingerprint,typeof(p.publication_authority_fingerprint)='text',p.author_crew_id,
 v.id,v.version,v.definition_json,v.authority_fingerprint,v.publication_authority_json,
 COALESCE(v.definition_json=p.definition_json AND v.authority_fingerprint=p.publication_authority_fingerprint,0)
 FROM main.pipelines p LEFT JOIN main.pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version
 WHERE p.id=? AND p.workspace_id=? AND p.deleted_at IS NULL AND p.status='active'`, id, workspace).Scan(&physical, &physicalText, &r.Crew, &versionID, &version, &raw, &fingerprint, &canonical, &sameBody)
	if err != nil {
		return nil, ref, manifest, false, err
	}
	if physicalText && physical.String == access.LegacyPublicationAuthorityFingerprint {
		return nil, ref, manifest, false, nil
	}
	if !physicalText || !versionID.Valid || !canonical.Valid || !sameBody {
		return nil, ref, manifest, true, ErrDenied
	}
	manifest, err = access.DecodePublicationAuthority([]byte(canonical.String), fingerprint.String)
	if err != nil || manifest.WorkspaceID != workspace || manifest.PipelineID != id || manifest.RootCrewID != r.Crew.String || len(manifest.Recipes) != 0 {
		return nil, ref, manifest, true, ErrDenied
	}
	r.Raw = raw.String
	r.Hash = hash(r.Raw)
	ref = publishedRef{VersionID: versionID.String, Version: int(version.Int64), Fingerprint: fingerprint.String, Publisher: manifest.PublisherID, Membership: manifest.PublisherMembershipID}
	return r, ref, manifest, true, nil
}

// compilePublishedGraph admits only the v1 subset frozen at publication:
// direct agent_run leaves on the recorded agents and allowlisted profiles.
func (s *Service) compilePublishedGraph(ctx context.Context, user, workspace string, root *graphRecipe, ref publishedRef, manifest access.PublicationAuthority) (frozenGraph, error) {
	var graph frozenGraph
	if err := ValidatePublicationDefinition(root.Raw); err != nil {
		return graph, err
	}
	d, err := compileTyped(root.Raw, true)
	if err != nil {
		return graph, err
	}
	if len(d.DSL.Steps) == 0 || len(d.DSL.Steps) > 16 || len(manifest.Agents) != len(d.DSL.Steps) {
		return graph, ErrUnsupported
	}
	bindings := map[string]access.PublicationAgentBinding{}
	for _, b := range manifest.Agents {
		if b.ParentVersionID != "" {
			return graph, ErrDenied
		}
		bindings[b.StepID] = b
	}
	store := access.Store{DB: s.db}
	profiles := map[string]string{}
	for _, step := range d.DSL.Steps {
		b, ok := bindings[step.ID]
		if !ok {
			return graph, ErrDenied
		}
		var profile string
		err = s.db.QueryRowContext(ctx, `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND crew_id=? AND slug=? AND deleted_at IS NULL`, b.AgentID, workspace, b.CrewID, step.AgentSlug).Scan(&profile)
		if err != nil {
			return graph, graphLookupError(err)
		}
		// The executable profile is the frozen v1 declaration, not a later edit.
		if profile != b.Profile || !s.supportsProfile(profile) || (profile != "responses_text" && profile != "native_api_key") {
			return graph, ErrDenied
		}
		g := graphStep{ID: step.ID, Agent: b.AgentID, Profile: profile}
		if g.ProviderHash, err = s.providers.ProviderDelegationHash(ctx, user, workspace, b.AgentID); err != nil {
			return graph, ErrDenied
		}
		root.Steps = append(root.Steps, g)
		profiles[b.AgentID] = profile
	}
	graph.Root, graph.Agent, graph.Publication = root, manifest.ActingAgentID, &ref
	if graph.Profile = profiles[graph.Agent]; graph.Profile == "" {
		return frozenGraph{}, ErrDenied
	}
	for agent := range profiles {
		graph.Rights = append(graph.Rights, access.Right{Kind: "agent", ID: agent, Operation: "run"})
		if agent != graph.Agent {
			right := access.Right{Kind: "agent", ID: agent, Operation: "delegate"}
			if store.Check(ctx, user, workspace, right) != nil {
				return frozenGraph{}, ErrDenied
			}
			graph.Rights = append(graph.Rights, right)
		}
	}
	graph.Rights = append(graph.Rights, access.Right{Kind: "routine", ID: root.ID, Operation: "run"})
	sortRights(graph.Rights)
	// The caller's own grants are checked by admission; the publisher's
	// frozen bindings must also be live now.
	if err = s.checkPublishedRecipe(ctx, s.db, workspace, user, tsformat.Format(time.Now()), root, ref); err != nil {
		return frozenGraph{}, err
	}
	return graph, nil
}

// publishedAuthorityQuery is the single per-boundary authority check for a
// published root: the exact HEAD publication, every frozen authoring and
// execution binding (same grant, live issuer, same unsuspended restricted
// publisher membership) and no administrator session revocation since the
// run was accepted. A user's own logout is deliberately not a stop reason.
const publishedAuthorityQuery = `WITH m AS (
 SELECT v.definition_json AS raw,p.author_crew_id AS crew,v.publication_authority_json AS manifest
 FROM main.pipelines p JOIN main.pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version
 WHERE p.id=? AND p.workspace_id=? AND p.deleted_at IS NULL AND p.status='active'
 AND typeof(p.publication_authority_fingerprint)='text' AND p.publication_authority_fingerprint=?
 AND v.id=? AND v.version=? AND v.authority_fingerprint=p.publication_authority_fingerprint
 AND v.definition_json=p.definition_json AND v.publication_authority_json IS NOT NULL),
b AS (SELECT e.value AS t FROM m,json_each(m.manifest,'$.authoring') e
 UNION ALL SELECT e.value FROM m,json_each(m.manifest,'$.execution') e)
SELECT m.raw,m.crew,(SELECT count(*) FROM b),(SELECT count(*) FROM b
 JOIN main.live_access_grants g ON g.id=json_extract(b.t,'$[6]') AND g.member_id=json_extract(b.t,'$[2]')
 AND g.created_by=json_extract(b.t,'$[7]') AND g.resource_kind=json_extract(b.t,'$[3]')
 AND COALESCE(g.agent_id,g.project_id,g.routine_id)=json_extract(b.t,'$[4]') AND g.operation=json_extract(b.t,'$[5]')
 JOIN main.workspace_members r ON r.id=g.member_id AND r.user_id=json_extract(b.t,'$[1]') AND r.workspace_id=json_extract(b.t,'$[0]') AND r.access_mode='restricted'
 JOIN main.users u ON u.id=r.user_id AND u.suspended_at IS NULL
 JOIN main.workspace_members i ON i.id=json_extract(b.t,'$[8]') AND i.user_id=g.created_by AND i.workspace_id=r.workspace_id),
 EXISTS(SELECT 1 FROM main.user_sessions s WHERE s.user_id IN (?,?) AND s.revoked_reason IN ('admin_revoke','admin_invalidate') AND julianday(s.revoked_at)>=julianday(?))
FROM m`

func (s *Service) checkPublishedRecipe(ctx context.Context, q rowQuery, workspace, principal, accepted string, r *graphRecipe, ref publishedRef) error {
	since, err := time.Parse(time.RFC3339Nano, accepted) // tsformat:allow: read-only admission timestamp parsing
	if err != nil {
		return ErrDenied
	}
	// Session revocations are stored at second precision.
	threshold := since.UTC().Truncate(time.Second).Format(time.RFC3339)
	var raw string
	var crew sql.NullString
	var total, live int
	var revoked bool
	err = q.QueryRowContext(ctx, publishedAuthorityQuery, r.ID, workspace, ref.Fingerprint, ref.VersionID, ref.Version, principal, ref.Publisher, threshold).Scan(&raw, &crew, &total, &live, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if raw != r.Raw || crew != r.Crew || total == 0 || live != total || revoked {
		return ErrDenied
	}
	return nil
}

func sortRights(rights []access.Right) {
	for i := 0; i < len(rights); i++ {
		for j := i + 1; j < len(rights); j++ {
			if rights[j].Kind+rights[j].ID+rights[j].Operation < rights[i].Kind+rights[i].ID+rights[i].Operation {
				rights[i], rights[j] = rights[j], rights[i]
			}
		}
	}
}

// Stop reasons are audit labels, never caller-visible authorization detail.
const (
	stopUnpublished        = "unpublished"
	stopPublicationChanged = "publication_changed"
	stopSuspended          = "account_suspended"
	stopMembership         = "membership_changed"
	stopSessions           = "sessions_revoked"
	stopIssuer             = "issuer_authority_lost"
	stopGrant              = "grant_revoked"
	stopDuration           = "max_duration"
	stopAuthorityChanged   = "authority_changed"
)

// publishedStopReason classifies a refused published run on the failure path
// only. It reads the in-memory job because member removal cascades its row.
func publishedStopReason(ctx context.Context, tx *sql.Tx, j job, g frozenGraph) string {
	ref := g.Publication
	var unpublished, changed, suspended, membership, sessions bool
	since, _ := time.Parse(time.RFC3339Nano, j.Created) // tsformat:allow: read-only admission timestamp parsing
	err := tx.QueryRowContext(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM pipelines WHERE id=? AND deleted_at IS NULL AND status='active'),
 NOT EXISTS(SELECT 1 FROM pipelines p JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version
  WHERE p.id=? AND p.publication_authority_fingerprint=? AND v.id=?),
 EXISTS(SELECT 1 FROM users WHERE id IN (?,?) AND suspended_at IS NOT NULL),
 NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND user_id=? AND access_mode='restricted')
  OR NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND user_id=? AND access_mode='restricted'),
 EXISTS(SELECT 1 FROM user_sessions WHERE user_id IN (?,?) AND revoked_reason IN ('admin_revoke','admin_invalidate') AND julianday(revoked_at)>=julianday(?))`,
		j.Pipeline, j.Pipeline, ref.Fingerprint, ref.VersionID, j.Principal, ref.Publisher, j.Member, j.Principal, ref.Membership, ref.Publisher,
		j.Principal, ref.Publisher, since.UTC().Truncate(time.Second).Format(time.RFC3339)).Scan(&unpublished, &changed, &suspended, &membership, &sessions)
	if err != nil {
		return stopAuthorityChanged
	}
	switch {
	case unpublished:
		return stopUnpublished
	case changed:
		return stopPublicationChanged
	case suspended:
		return stopSuspended
	case sessions:
		return stopSessions
	}
	// Grants of both the caller and the frozen publisher bindings.
	type grant struct{ member, kind, id, operation string }
	var grants []grant
	for _, right := range g.Rights {
		grants = append(grants, grant{j.Member, right.Kind, right.ID, right.Operation})
	}
	var canonical string
	if tx.QueryRowContext(ctx, `SELECT publication_authority_json FROM pipeline_versions WHERE id=?`, ref.VersionID).Scan(&canonical) == nil {
		if manifest, err := access.DecodePublicationAuthority([]byte(canonical), ref.Fingerprint); err == nil {
			for _, b := range append(manifest.Authoring, manifest.Execution...) {
				grants = append(grants, grant{b.PublisherMembershipID, b.Right.Kind, b.Right.ID, b.Right.Operation})
			}
		}
	}
	for _, x := range grants {
		var stored, live bool
		if tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM access_grants WHERE member_id=? AND resource_kind=? AND COALESCE(agent_id,project_id,routine_id)=? AND operation=?),
 EXISTS(SELECT 1 FROM live_access_grants WHERE member_id=? AND resource_kind=? AND COALESCE(agent_id,project_id,routine_id)=? AND operation=?)`,
			x.member, x.kind, x.id, x.operation, x.member, x.kind, x.id, x.operation).Scan(&stored, &live) != nil {
			return stopAuthorityChanged
		}
		if stored && !live {
			return stopIssuer
		}
		if !stored {
			if membership {
				return stopMembership
			}
			return stopGrant
		}
	}
	if membership {
		return stopMembership
	}
	if expires, err := time.Parse(time.RFC3339Nano, j.Expires); err == nil && !time.Now().Before(expires) { // tsformat:allow: read-only deadline parsing
		return stopDuration
	}
	return stopAuthorityChanged
}

// auditPublishedRun appends a chained journal event in the caller's writer.
// It carries identities and reasons only, never inputs or outputs.
func (s *Service) auditPublishedRun(ctx context.Context, tx *sql.Tx, j job, g frozenGraph, action, reason string) (func(), error) {
	payload := map[string]any{"entity_type": "routine_run", "action": action, "run_id": j.ID, "pipeline_id": j.Pipeline,
		"version": g.Publication.Version, "version_id": g.Publication.VersionID, "authority_fingerprint": g.Publication.Fingerprint, "publisher_id": g.Publication.Publisher}
	entry := journal.Entry{WorkspaceID: j.Workspace, Type: journal.EntryAuditEntityCreated, Severity: journal.SeverityNotice,
		ActorType: journal.ActorUser, ActorID: j.Principal, Summary: "Guest started a published routine run", Payload: payload,
		Refs: map[string]any{"entity_type": "routine", "entity_id": j.Pipeline}}
	if action == "published_run_stopped" {
		payload["reason"] = reason
		entry.Type, entry.ActorType, entry.ActorID, entry.Summary = journal.EntryAuditEntityUpdated, journal.ActorSystem, "restricted-workflow", "Published routine run stopped: "+reason
	}
	if s.Audit != nil {
		return s.Audit.AppendTx(ctx, tx, entry)
	}
	return journal.AppendTx(ctx, tx, entry)
}
