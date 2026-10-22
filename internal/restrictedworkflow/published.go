package restrictedworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

// DefaultPublishedRunLimit bounds one Guest-published run from its scheduled
// start unless the workspace sets guest_routine_max_run_minutes (1-240). The
// job deadline is immutable once accepted.
const DefaultPublishedRunLimit = 30 * time.Minute

func (s *Service) publishedRunLimitFor(ctx context.Context, workspace string) (time.Duration, error) {
	if s.publishedRunLimit > 0 {
		return s.publishedRunLimit, nil
	}
	var minutes int
	if err := s.db.QueryRowContext(ctx, `SELECT guest_routine_max_run_minutes FROM workspaces WHERE id=? AND deleted_at IS NULL`, workspace).Scan(&minutes); err != nil {
		return 0, graphLookupError(err)
	}
	if minutes < 1 || minutes > 240 {
		return 0, ErrDenied
	}
	return time.Duration(minutes) * time.Minute, nil
}

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
 COALESCE(v.definition_json=p.definition_json AND v.authority_fingerprint=p.publication_authority_fingerprint AND p.publication_withdrawn_at IS NULL,0)
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
	if err = s.checkPublishedRecipe(ctx, s.db, workspace, user, root, ref); err != nil {
		return frozenGraph{}, err
	}
	return graph, nil
}

// publishedCTE selects the exact HEAD publication and its frozen authoring
// and execution grant bindings. Parameters: pipeline, workspace, fingerprint,
// version ID, version.
const publishedCTE = `WITH m AS (
 SELECT v.definition_json AS raw,p.author_crew_id AS crew,v.publication_authority_json AS manifest
 FROM main.pipelines p JOIN main.pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version
 WHERE p.id=? AND p.workspace_id=? AND p.deleted_at IS NULL AND p.status='active' AND p.publication_withdrawn_at IS NULL
 AND typeof(p.publication_authority_fingerprint)='text' AND p.publication_authority_fingerprint=?
 AND v.id=? AND v.version=? AND v.authority_fingerprint=p.publication_authority_fingerprint
 AND v.definition_json=p.definition_json AND v.publication_authority_json IS NOT NULL),
b AS (SELECT e.value AS t FROM m,json_each(m.manifest,'$.authoring') e
 UNION ALL SELECT e.value FROM m,json_each(m.manifest,'$.execution') e)
`

// publishedColumns: every binding is the same live grant, issuer and
// unsuspended restricted publisher membership; the administrator revocation
// marker of the caller and publisher; and the current provider declaration
// of every recorded agent. Parameters: principal, publisher, agents JSON,
// workspace.
const publishedColumns = `m.raw,m.crew,m.manifest,(SELECT count(*) FROM b),(SELECT count(*) FROM b
 JOIN main.live_access_grants g ON g.id=json_extract(b.t,'$[6]') AND g.member_id=json_extract(b.t,'$[2]')
 AND g.created_by=json_extract(b.t,'$[7]') AND g.resource_kind=json_extract(b.t,'$[3]')
 AND COALESCE(g.agent_id,g.project_id,g.routine_id)=json_extract(b.t,'$[4]') AND g.operation=json_extract(b.t,'$[5]')
 JOIN main.workspace_members r ON r.id=g.member_id AND r.user_id=json_extract(b.t,'$[1]') AND r.workspace_id=json_extract(b.t,'$[0]') AND r.access_mode='restricted'
 JOIN main.users u ON u.id=r.user_id AND u.suspended_at IS NULL
 JOIN main.workspace_members i ON i.id=json_extract(b.t,'$[8]') AND i.user_id=g.created_by AND i.workspace_id=r.workspace_id),
 (SELECT COALESCE(max(access_revoked_at),'') FROM main.users WHERE id IN (?,?)),
 (SELECT json_group_array(json_array(a.id,c.id,ac.id,c.encrypted_value,a.llm_model,a.restricted_execution_profile))
  FROM main.agents a JOIN main.agent_credentials ac ON ac.agent_id=a.id
  JOIN main.credentials c ON c.id=ac.credential_id AND c.workspace_id=a.workspace_id
  WHERE a.id IN (SELECT value FROM json_each(?)) AND a.workspace_id=? AND a.deleted_at IS NULL AND a.llm_provider='OPENAI'
  AND c.deleted_at IS NULL AND c.status='ACTIVE' AND c.provider='OPENAI' AND c.type='API_KEY'
  AND c.security_level IN (1,2) AND ac.env_var_name='OPENAI_API_KEY')`

type publishedState struct {
	raw, manifest, marker, providers string
	crew                             sql.NullString
	total, live                      int
}

func (st *publishedState) targets() []any {
	return []any{&st.raw, &st.crew, &st.manifest, &st.total, &st.live, &st.marker, &st.providers}
}

func publishedArgs(r *graphRecipe, workspace, principal string, ref publishedRef, agents string) []any {
	return []any{r.ID, workspace, ref.Fingerprint, ref.VersionID, ref.Version, principal, ref.Publisher, agents, workspace}
}

func graphAgents(r *graphRecipe) string {
	ids := []string{}
	for _, step := range r.Steps {
		ids = append(ids, step.Agent)
	}
	raw, _ := json.Marshal(ids)
	return string(raw)
}

// providerRevision mirrors restricteddispatch's credential revision: the
// manifest records it at publication, so rotation changes it.
func providerRevision(credential, ciphertext, profile string) string {
	return hash(credential + "\x00" + ciphertext + "\x00" + profile)
}

// ErrPublishedDeferral refuses a delayed start of a Guest-published routine.
var ErrPublishedDeferral = errors.New("published routines run immediately; delayed start is not supported")

// errProviderChanged distinguishes a rotated or edited provider declaration.
var errProviderChanged = errors.New("published provider declaration changed")

// verify checks the selected state against the frozen root. accepted is the
// run's acceptance time; an administrator revocation at or after it stops
// the run. An empty accepted (pre-admission preview) skips that marker.
func (st publishedState) verify(r *graphRecipe, ref publishedRef, accepted string) error {
	if st.raw != r.Raw || st.crew != r.Crew || st.total == 0 || st.live != st.total {
		return ErrDenied
	}
	if accepted != "" && st.marker != "" && st.marker >= accepted {
		return ErrDenied
	}
	manifest, err := access.DecodePublicationAuthority([]byte(st.manifest), ref.Fingerprint)
	if err != nil || manifest.PublisherID != ref.Publisher {
		return ErrDenied
	}
	var rows [][6]string
	if json.Unmarshal([]byte(st.providers), &rows) != nil {
		return ErrDenied
	}
	current := map[string][6]string{}
	for _, row := range rows {
		if _, duplicate := current[row[0]]; duplicate {
			return errProviderChanged
		}
		current[row[0]] = row
	}
	// The frozen agent bindings: credential, grant, credential revision,
	// model and profile must all be the ones recorded at publication.
	for _, b := range manifest.Agents {
		row, ok := current[b.AgentID]
		if !ok || row[1] != b.CredentialID || row[2] != b.GrantID || providerRevision(row[1], row[3], row[5]) != b.CredentialRevision || row[4] != b.Model || row[5] != b.Profile {
			return errProviderChanged
		}
	}
	return nil
}

// checkPublishedRecipe previews a publication before a job exists (catalog
// and admission). Running jobs use checkPublishedJob instead.
func (s *Service) checkPublishedRecipe(ctx context.Context, q rowQuery, workspace, principal string, r *graphRecipe, ref publishedRef) error {
	var st publishedState
	err := q.QueryRowContext(ctx, publishedCTE+`SELECT `+publishedColumns+` FROM m`, publishedArgs(r, workspace, principal, ref, graphAgents(r))...).Scan(st.targets()...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if err = st.verify(r, ref, ""); err != nil {
		return ErrDenied
	}
	return nil
}

// boundary optionally folds the dispatch lease and the run's parent attempt
// into the same query at execution boundaries.
type boundary struct {
	parent     string
	assignment *dispatch.Assignment
}

// checkPublishedJob is the whole authority check for an accepted published
// run, in ONE query: origin attempt, member revision, principal, chat, agent
// profile, the caller's rights, the publication and its bindings, the
// administrator revocation marker, provider declarations, the frozen recipe
// and provider slots, and optionally the lease and parent attempt. It runs at
// every step boundary, immediately before each provider call.
func (s *Service) checkPublishedJob(ctx context.Context, q rowQuery, j job, handle string, g frozenGraph, at boundary) error {
	expires, err := time.Parse(time.RFC3339Nano, j.Expires) // tsformat:allow: read-only deadline parsing
	if err != nil || ((j.State == "pending" || j.State == "running") && !time.Now().Before(expires)) {
		return ErrDenied
	}
	if j.Source != "manual" || j.SourceFacet != "" || g.Root == nil || hash(j.Recipe) != j.RecipeHash {
		return ErrDenied
	}
	var rights []access.Right
	if json.Unmarshal([]byte(j.Rights), &rights) != nil || len(rights) == 0 {
		return ErrDenied
	}
	for _, right := range rights {
		if right.ID == "" || !((right.Kind == "agent" && (right.Operation == "run" || right.Operation == "delegate")) || (right.Kind == "routine" && right.Operation == "run" && right.ID == j.Pipeline)) {
			return ErrDenied
		}
	}
	item, generation, run, lease := "", int64(0), "", tsformat.Format(time.Now())
	if at.assignment != nil {
		item, generation, run = at.assignment.Item.ID, at.assignment.Generation, at.assignment.RunID
	}
	var st publishedState
	var origin, granted, bound bool
	var storedGraph, policies string
	var leased, parent bool
	args := append(publishedArgs(g.Root, j.Workspace, j.Principal, *g.Publication, graphAgents(g.Root)),
		j.Profile, j.Pipeline, j.Recipe, j.Origin, hash(handle), j.Workspace, j.Principal, j.Member, j.Revision, j.Agent, j.Chat,
		j.Rights, j.Member,
		j.ID, j.ID, j.ID, j.Pipeline, g.Root.Hash, g.Root.Raw, g.Root.Crew,
		j.ID,
		item, item, workflowDomain, j.ID, generation, run, lease,
		at.parent, at.parent, j.Principal, j.Chat, j.Member, j.Revision)
	err = q.QueryRowContext(ctx, publishedCTE+`SELECT `+publishedColumns+`,
 EXISTS(SELECT 1 FROM main.access_attempts a
  JOIN main.workspace_members wm ON wm.id=a.member_id AND wm.workspace_id=a.workspace_id AND wm.user_id=a.principal_id AND wm.access_revision=a.member_revision AND wm.access_mode='restricted'
  JOIN main.users principal ON principal.id=a.principal_id AND principal.suspended_at IS NULL
  JOIN main.workspaces w ON w.id=a.workspace_id AND w.deleted_at IS NULL
  JOIN main.chats c ON c.id=a.chat_id AND c.workspace_id=a.workspace_id AND c.agent_id=a.agent_id AND c.created_by=a.principal_id AND c.visibility='private' AND c.authority_generation=a.chat_generation AND c.authority_revision=a.chat_revision
  JOIN main.agents ag ON ag.id=a.agent_id AND ag.workspace_id=a.workspace_id AND ag.deleted_at IS NULL AND ag.restricted_execution_profile=?
  JOIN main.pipelines p ON p.id=? AND p.workspace_id=a.workspace_id AND p.deleted_at IS NULL AND p.status='active' AND p.definition_json=?
  WHERE a.id=? AND a.handle_hash=? AND a.workspace_id=? AND a.principal_id=? AND a.member_id=? AND a.member_revision=? AND a.agent_id=? AND a.chat_id=? AND a.revoked_at IS NULL AND a.completed_at IS NOT NULL AND a.parent_id IS NULL AND a.admission_operation='run' AND a.context_audience=''),
 NOT EXISTS(SELECT 1 FROM json_each(?) r WHERE NOT EXISTS(SELECT 1 FROM main.live_access_grants g WHERE g.member_id=? AND g.resource_kind=json_extract(r.value,'$.kind') AND COALESCE(g.agent_id,g.project_id,g.routine_id)=json_extract(r.value,'$.id') AND g.operation=json_extract(r.value,'$.operation'))),
 COALESCE((SELECT graph_hash FROM main.restricted_workflow_jobs WHERE id=?),''),
 NOT EXISTS(SELECT 1 FROM main.restricted_workflow_jobs WHERE id=?) OR EXISTS(SELECT 1 FROM main.restricted_workflow_recipe_bindings WHERE job_id=? AND pipeline_id=? AND recipe_hash=? AND recipe_json=? AND author_crew_id IS ?),
 (SELECT json_group_array(json_array(agent_id,policy_hash,expires_at)) FROM main.restricted_workflow_provider_policies WHERE job_id=? AND max_output_tokens>=4096),
 ?='' OR EXISTS(SELECT 1 FROM main.work_items wi JOIN main.work_attempts wa ON wa.work_id=wi.id AND wa.generation=wi.generation
  WHERE wi.id=? AND wi.domain_kind=? AND wi.domain_id=? AND wi.generation=? AND wa.run_id=? AND wi.state IN ('starting','running') AND wa.ended_at IS NULL AND wa.lease_expires_at>?),
 ?='' OR EXISTS(SELECT 1 FROM main.access_attempts pa WHERE pa.id=? AND pa.revoked_at IS NULL AND pa.principal_id=? AND pa.chat_id=? AND pa.member_id=? AND pa.member_revision=?)
 FROM m`, args...).Scan(append(st.targets(), &origin, &granted, &storedGraph, &bound, &policies, &leased, &parent)...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	if !leased {
		return work.ErrStaleGeneration
	}
	if !origin || !granted || !bound || !parent || (storedGraph != "" && storedGraph != j.GraphHash) {
		return ErrDenied
	}
	if err = st.verify(g.Root, *g.Publication, j.Created); err != nil {
		return ErrDenied
	}
	if storedGraph != "" {
		var slots [][3]string
		if json.Unmarshal([]byte(policies), &slots) != nil {
			return ErrDenied
		}
		frozen := map[string]string{}
		for _, slot := range slots {
			deadline, err := time.Parse(time.RFC3339Nano, slot[2]) // tsformat:allow: read-only immutable policy deadline validation
			if err != nil || !deadline.After(time.Now()) {
				return ErrDenied
			}
			frozen[slot[0]] = slot[1]
		}
		for _, step := range g.Root.Steps {
			if step.Child != nil || frozen[step.Agent] != step.ProviderHash || !s.supportsProfile(step.Profile) {
				return ErrDenied
			}
		}
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
	stopProvider           = "provider_changed"
	stopDuration           = "max_duration"
	stopAuthorityChanged   = "authority_changed"
)

// publishedStopReason classifies a refused published run on the failure path
// only. It reads the in-memory job because member removal cascades its row.
func publishedStopReason(ctx context.Context, tx *sql.Tx, j job, g frozenGraph) string {
	ref := g.Publication
	var unpublished, changed, suspended, membership, sessions bool
	err := tx.QueryRowContext(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM pipelines WHERE id=? AND deleted_at IS NULL AND status='active' AND publication_withdrawn_at IS NULL),
 NOT EXISTS(SELECT 1 FROM pipelines p JOIN pipeline_versions v ON v.pipeline_id=p.id AND v.version=p.head_version
  WHERE p.id=? AND p.publication_authority_fingerprint=? AND v.id=?),
 EXISTS(SELECT 1 FROM users WHERE id IN (?,?) AND suspended_at IS NOT NULL),
 NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND user_id=? AND access_mode='restricted')
  OR NOT EXISTS(SELECT 1 FROM workspace_members WHERE id=? AND user_id=? AND access_mode='restricted'),
 EXISTS(SELECT 1 FROM users WHERE id IN (?,?) AND access_revoked_at>=?)`,
		j.Pipeline, j.Pipeline, ref.Fingerprint, ref.VersionID, j.Principal, ref.Publisher, j.Member, j.Principal, ref.Membership, ref.Publisher,
		j.Principal, ref.Publisher, j.Created).Scan(&unpublished, &changed, &suspended, &membership, &sessions)
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
	var st publishedState
	if tx.QueryRowContext(ctx, publishedCTE+`SELECT `+publishedColumns+` FROM m`, publishedArgs(g.Root, j.Workspace, j.Principal, *ref, graphAgents(g.Root))...).Scan(st.targets()...) == nil {
		if errors.Is(st.verify(g.Root, *ref, ""), errProviderChanged) {
			return stopProvider
		}
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
