package restrictedworkflow

import (
	"context"

	"github.com/crewship-ai/crewship/internal/access"
)

// CatalogRoutine is the minimal invocation contract, never the stored recipe,
// provider configuration, step prompts, or input default values.
type CatalogRoutine struct {
	Slug           string         `json:"slug"`
	Name           string         `json:"name"`
	DefinitionHash string         `json:"definition_hash"`
	Inputs         []CatalogInput `json:"inputs"`
}
type CatalogInput struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Required bool   `json:"required"`
}

func (s *Service) Catalog(ctx context.Context, user, workspace string) ([]CatalogRoutine, error) {
	store := access.Store{DB: s.db}
	member, err := store.Membership(ctx, user, workspace)
	if err != nil || member.Mode != "restricted" || manualPolicy(ctx, s.db, user, workspace) != nil {
		return nil, ErrDenied
	}
	if _, ok := s.executor.(proofExecutor); !ok {
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
		item := CatalogRoutine{Slug: c.slug, Name: c.name, DefinitionHash: graph.Root.Hash, Inputs: []CatalogInput{}}
		for _, input := range d.DSL.Inputs {
			item.Inputs = append(item.Inputs, CatalogInput{Name: input.Name, Type: input.Type, Required: input.Required && input.Default == nil})
		}
		result = append(result, item)
	}
	// Recheck the directory's captured membership epoch before exposing metadata.
	current, err := store.Membership(ctx, user, workspace)
	if err != nil || current != member || manualPolicy(ctx, s.db, user, workspace) != nil {
		return nil, ErrDenied
	}
	return result, nil
}
