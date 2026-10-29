package restrictedworkflow

import (
	"context"
	"database/sql"
	"errors"
	"sort"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// PublicationProvider reads host-selected declarations, never request credentials.
type PublicationProvider interface {
	PublicationProviderIn(context.Context, *sql.Tx, string, string, string) (access.PublicationProviderSnapshot, error)
}

// CompilePublicationIn captures v1 authority in the host writer transaction.
// The authenticated publisher and captured identity grant no execution admission.
func CompilePublicationIn(ctx context.Context, db *sql.DB, tx *sql.Tx, publisher, workspace, routineID, definitionRaw string) (access.PublicationAuthority, []byte, string, error) {
	deny := func(err error) (access.PublicationAuthority, []byte, string, error) {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, runauthority.ErrRequesterStale) || errors.Is(err, runauthority.ErrIdentityMismatch) || errors.Is(err, runauthority.ErrUnavailable) {
			err = access.ErrDenied
		}
		return access.PublicationAuthority{}, nil, "", err
	}
	if ctx == nil || db == nil || tx == nil || publisher == "" || workspace == "" || routineID == "" {
		return deny(access.ErrDenied)
	}
	provider, ok := workflowProviderAuthority(db).(PublicationProvider)
	if !ok || provider == nil {
		return deny(access.ErrDenied)
	}
	if err := ValidatePublicationDefinition(definitionRaw); err != nil {
		return deny(err)
	}
	d, err := compileTyped(definitionRaw, true)
	if err != nil {
		return deny(err)
	}
	var membership string
	err = tx.QueryRowContext(ctx, `SELECT wm.id FROM main.workspace_members wm
 JOIN main.users u ON u.id=wm.user_id AND u.suspended_at IS NULL
 JOIN main.workspaces w ON w.id=wm.workspace_id AND w.deleted_at IS NULL
 WHERE wm.user_id=? AND wm.workspace_id=? AND wm.access_mode='restricted'`, publisher, workspace).Scan(&membership)
	if err != nil {
		return deny(err)
	}
	var crew, author, origin string
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(author_crew_id,''),COALESCE(author_agent_id,''),COALESCE(chat_origin_id,'')
 FROM main.pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL
 AND (author_crew_id IS NULL OR (typeof(author_crew_id)='text' AND author_crew_id!=''))
 AND (author_agent_id IS NULL OR (typeof(author_agent_id)='text' AND author_agent_id!=''))
 AND (chat_origin_id IS NULL OR (typeof(chat_origin_id)='text' AND chat_origin_id!=''))`, routineID, workspace).Scan(&crew, &author, &origin)
	if err != nil {
		return deny(err)
	}
	if origin != "" {
		if _, err = runauthority.ValidateOriginReceipt(ctx, tx, origin, workspace); err != nil {
			return deny(err)
		}
	}
	a := access.PublicationAuthority{
		Schema: access.PublicationAuthoritySchema, WorkspaceID: workspace,
		PipelineID: routineID, PublisherID: publisher, PublisherMembershipID: membership,
		RootCrewID: crew, AuthorAgentID: author, RootOriginID: origin,
	}
	store := access.Store{DB: db}
	for _, operation := range []string{"view", "edit", "publish"} {
		binding, err := store.CapturePublicationGrantIn(ctx, tx, publisher, workspace, access.Right{Kind: "routine", ID: routineID, Operation: operation})
		if err != nil {
			return deny(err)
		}
		a.Authoring = append(a.Authoring, binding)
	}
	// A stable callsite order selects the acting leaf; neither input order nor
	// caller-supplied author metadata can choose a different delegation root.
	sort.Slice(d.DSL.Steps, func(i, j int) bool { return d.DSL.Steps[i].ID < d.DSL.Steps[j].ID })
	seenRights := map[access.Right]bool{}
	for _, step := range d.DSL.Steps {
		rows, err := tx.QueryContext(ctx, `SELECT a.id,a.crew_id FROM main.agents a
 JOIN main.crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
 WHERE a.workspace_id=? AND a.slug=? AND a.deleted_at IS NULL
 AND (?='' OR a.crew_id=?) LIMIT 2`, workspace, step.AgentSlug, crew, crew)
		if err != nil {
			return deny(err)
		}
		var agent, agentCrew string
		count := 0
		for rows.Next() {
			count++
			if err = rows.Scan(&agent, &agentCrew); err != nil {
				break
			}
		}
		err = errors.Join(err, rows.Err(), rows.Close())
		if err != nil {
			return deny(err)
		}
		if count != 1 {
			return deny(access.ErrDenied)
		}
		if a.ActingAgentID == "" {
			a.ActingAgentID = agent
		}
		operations := []string{"run"}
		if agent != a.ActingAgentID {
			operations = append(operations, "delegate")
		}
		for _, operation := range operations {
			right := access.Right{Kind: "agent", ID: agent, Operation: operation}
			if seenRights[right] {
				continue
			}
			binding, err := store.CapturePublicationGrantIn(ctx, tx, publisher, workspace, right)
			if err != nil {
				return deny(err)
			}
			a.Execution = append(a.Execution, binding)
			seenRights[right] = true
		}
		declaration, err := provider.PublicationProviderIn(ctx, tx, workspace, agentCrew, agent)
		if err != nil {
			return deny(err)
		}
		if declaration.WorkspaceID != workspace || declaration.AgentID != agent || declaration.CrewID != agentCrew {
			return deny(access.ErrDenied)
		}
		// The v1 policy is a fixed allowlist, independent of executor plugins.
		if declaration.Profile != "responses_text" && declaration.Profile != "native_api_key" {
			return deny(ErrUnsupported)
		}
		a.Agents = append(a.Agents, access.PublicationAgentBinding{
			StepID: step.ID, AgentID: agent, CrewID: agentCrew,
			Profile: declaration.Profile, PolicyHash: declaration.PolicyHash,
			CredentialID: declaration.CredentialID, GrantID: declaration.GrantID,
			CredentialRevision: declaration.CredentialRevision, Model: declaration.Model,
			GrantExpiry: declaration.GrantExpiry, KeyExpiry: declaration.KeyExpiry,
			MaxOutputTokens: 4096,
		})
	}
	canonical, fingerprint, err := access.CanonicalPublicationAuthority(a)
	if err != nil {
		return deny(err)
	}
	return a, canonical, fingerprint, nil
}
