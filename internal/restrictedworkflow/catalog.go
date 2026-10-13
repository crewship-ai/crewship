package restrictedworkflow

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/access"
)

// CatalogRoutine is the minimal invocation contract, never the stored recipe,
// provider configuration, step prompts, or input default values.
type CatalogRoutine struct {
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	DefinitionHash string         `json:"definition_hash"`
	ExecutionHash  string         `json:"execution_hash"`
	Inputs         []CatalogInput `json:"inputs"`
}
type CatalogInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

func (s *Service) Catalog(ctx context.Context, user, workspace string) ([]CatalogRoutine, error) {
	if _, ok := s.executor.(proofExecutor); !ok {
		return nil, ErrDenied
	}
	return s.authorizedCatalog(ctx, user, workspace, false)
}

// AuthorizedCatalog evaluates visibility without requiring an installed execution runtime.
// It shares all membership, recipe, graph and provider-authority checks with Catalog.
func AuthorizedCatalog(ctx context.Context, db *sql.DB, user, workspace string) ([]CatalogRoutine, error) {
	s := &Service{db: db, providers: workflowProviderAuthority(db), authorityOnly: true}
	return s.authorizedCatalog(ctx, user, workspace, false)
}

// HasAuthorizedRoutine stops at the first authorized recipe, sharing the same
// graph and epoch checks as the catalog without freezing every visible item.
func HasAuthorizedRoutine(ctx context.Context, db *sql.DB, user, workspace string, profiles func(string) bool) (bool, error) {
	s := &Service{db: db, providers: workflowProviderAuthority(db), authorityOnly: true, declaredProfiles: profiles}
	items, err := s.authorizedCatalog(ctx, user, workspace, true)
	return len(items) > 0, err
}

func (s *Service) authorizedCatalog(ctx context.Context, user, workspace string, firstOnly bool) ([]CatalogRoutine, error) {
	store := access.Store{DB: s.db}
	member, err := store.Membership(ctx, user, workspace)
	if err != nil || member.Mode != "restricted" || manualPolicy(ctx, s.db, user, workspace) != nil {
		return nil, ErrDenied
	}

	rows, err := s.db.QueryContext(ctx, `SELECT id,slug,name FROM pipelines WHERE workspace_id=? AND status='active' AND deleted_at IS NULL ORDER BY slug`, workspace)
	if err != nil {
		return nil, err
	}
	type candidate struct{ id, slug, name string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.slug, &c.name); err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []CatalogRoutine{}
	for _, c := range candidates {
		graph, err := s.compileGraph(ctx, user, workspace, c.id)
		if err != nil {
			continue
		}
		allowed := true
		for _, right := range graph.Rights {
			if store.Check(ctx, user, workspace, right) != nil {
				allowed = false
				break
			}
		}
		if !allowed {
			continue
		}
		d, err := compileTyped(graph.Root.Raw, true)
		if err != nil {
			continue
		}
		rawGraph, err := json.Marshal(graph)
		if err != nil {
			return nil, err
		}
		item := CatalogRoutine{ExecutionHash: hash(string(rawGraph)), Slug: c.slug, Name: c.name, DefinitionHash: graph.Root.Hash, Inputs: []CatalogInput{}}
		for _, input := range d.DSL.Inputs {
			item.Inputs = append(item.Inputs, CatalogInput{Name: input.Name, Type: input.Type, Required: input.Required && input.Default == nil})
		}
		result = append(result, item)
		if firstOnly {
			break
		}
	}
	// Recheck the directory's captured membership epoch before exposing metadata.
	current, err := store.Membership(ctx, user, workspace)
	if err != nil || current != member || manualPolicy(ctx, s.db, user, workspace) != nil {
		return nil, ErrDenied
	}
	return result, nil
}

// HasAnyWorkflowRunAuthority checks only a necessary condition. Every supported
// graph contains an agent leaf (compileGraph rejects leaves==0); true never
// replaces graph/provider/Page authorization. This is request-local, not a cache.
func HasAnyWorkflowRunAuthority(ctx context.Context, db *sql.DB, user, workspace string) (bool, error) {
	store := access.Store{DB: db}
	member, err := store.Membership(ctx, user, workspace)
	if err != nil {
		return false, err
	}
	eligible := false
	if member.Mode == "restricted" && manualPolicy(ctx, db, user, workspace) == nil {
		err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM access_grants g JOIN agents a ON a.id=g.agent_id WHERE g.member_id=? AND g.resource_kind='agent' AND g.operation='run' AND a.workspace_id=? AND a.deleted_at IS NULL)`, member.ID, workspace).Scan(&eligible)
		if err != nil {
			return false, err
		}
	}
	current, err := store.Membership(ctx, user, workspace)
	if err != nil {
		return false, err
	}
	if current != member {
		return false, ErrDenied
	}
	return eligible, nil
}
