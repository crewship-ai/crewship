package restrictedworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
)

type providerAuthority interface {
	ProviderDelegationHash(context.Context, string, string, string) (string, error)
	FreezeWorkflowProvider(context.Context, *sql.Tx, string, string, string, int64) error
	BindProviderDelegation(context.Context, string, string, string, string, int64) error
}

// FrozenGraph is internal queue data, never a routine directory projection.
type frozenGraph struct {
	Root    *graphRecipe   `json:"root"`
	Agent   string         `json:"agent"`
	Profile string         `json:"profile"`
	Rights  []access.Right `json:"rights"`
}
type graphRecipe struct {
	ID, Hash, Raw string
	Crew          sql.NullString
	Steps         []graphStep
}
type graphStep struct {
	ID, Agent, Profile, ProviderHash string
	Child                            *graphRecipe
}

func decodeGraph(j job) (frozenGraph, error) {
	var g frozenGraph
	if j.Graph == "" || len(j.Graph) > 1<<20 || hash(j.Graph) != j.GraphHash || json.Unmarshal([]byte(j.Graph), &g) != nil || g.Root == nil || g.Root.ID != j.Pipeline || g.Root.Raw != j.Recipe || g.Root.Hash != j.RecipeHash || g.Agent != j.Agent || g.Profile != j.Profile {
		return g, ErrDenied
	}
	return g, nil
}

func (s *Service) checkGraph(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, j job) error {
	g, err := decodeGraph(j)
	if err != nil {
		return err
	}
	var storedHash string
	err = q.QueryRowContext(ctx, `SELECT graph_hash FROM restricted_workflow_jobs WHERE id=?`, j.ID).Scan(&storedHash)
	stored := err == nil
	if (err != nil && !errors.Is(err, sql.ErrNoRows)) || (stored && storedHash != j.GraphHash) {
		return ErrDenied
	}
	seenRecipes := map[string]string{}
	seenAgents := map[string]string{}
	var visit func(*graphRecipe, int) error
	visit = func(r *graphRecipe, depth int) error {
		if r == nil || depth > 4 || len(seenRecipes) > 64 || hash(r.Raw) != r.Hash {
			return ErrDenied
		}
		if previous, ok := seenRecipes[r.ID]; ok {
			if previous != r.Hash {
				return ErrDenied
			}
		} else {
			seenRecipes[r.ID] = r.Hash
			var one int
			if q.QueryRowContext(ctx, `SELECT 1 FROM pipelines WHERE id=? AND workspace_id=? AND definition_json=? AND author_crew_id IS ? AND deleted_at IS NULL AND status='active'`, r.ID, j.Workspace, r.Raw, r.Crew).Scan(&one) != nil {
				return ErrDenied
			}
			if stored && q.QueryRowContext(ctx, `SELECT 1 FROM restricted_workflow_recipe_bindings WHERE job_id=? AND pipeline_id=? AND recipe_hash=? AND recipe_json=? AND author_crew_id IS ?`, j.ID, r.ID, r.Hash, r.Raw, r.Crew).Scan(&one) != nil {
				return ErrDenied
			}
		}
		for _, step := range r.Steps {
			if step.Child != nil {
				if err := visit(step.Child, depth+1); err != nil {
					return err
				}
				continue
			}
			if old, ok := seenAgents[step.Agent]; ok {
				if old != step.ProviderHash {
					return ErrDenied
				}
				continue
			}
			seenAgents[step.Agent] = step.ProviderHash
			if len(seenAgents) > 16 {
				return ErrDenied
			}
			var profile string
			if q.QueryRowContext(ctx, `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, step.Agent, j.Workspace).Scan(&profile) != nil || profile != step.Profile {
				return ErrDenied
			}
			if stored {
				var expiry string
				if q.QueryRowContext(ctx, `SELECT expires_at FROM restricted_workflow_provider_policies WHERE job_id=? AND agent_id=? AND policy_hash=? AND max_output_tokens>=4096`, j.ID, step.Agent, step.ProviderHash).Scan(&expiry) != nil {
					return ErrDenied
				}
				deadline, err := time.Parse(time.RFC3339Nano, expiry) // tsformat:allow: read-only immutable policy deadline validation, never a SQL timestamp write
				if err != nil || !deadline.After(time.Now()) {
					return ErrDenied
				}
			} else {
				current, err := s.providers.ProviderDelegationHash(ctx, j.Principal, j.Workspace, step.Agent)
				if err != nil || current != step.ProviderHash {
					return ErrDenied
				}
			}
		}
		return nil
	}
	return visit(g.Root, 1)
}

func (s *Service) freezeGraph(ctx context.Context, tx *sql.Tx, j job) error {
	g, err := decodeGraph(j)
	if err != nil || s.providers == nil {
		return ErrDenied
	}
	seenRecipes := map[string]string{}
	seenAgents := map[string]string{}
	var visit func(*graphRecipe) error
	visit = func(r *graphRecipe) error {
		if previous, ok := seenRecipes[r.ID]; ok {
			if previous != r.Hash {
				return ErrDenied
			}
		} else {
			seenRecipes[r.ID] = r.Hash
			if _, err := tx.ExecContext(ctx, `INSERT INTO restricted_workflow_recipe_bindings(job_id,pipeline_id,recipe_hash,recipe_json,author_crew_id) VALUES(?,?,?,?,?)`, j.ID, r.ID, r.Hash, r.Raw, r.Crew); err != nil {
				return err
			}
		}
		for _, step := range r.Steps {
			if step.Child != nil {
				if err := visit(step.Child); err != nil {
					return err
				}
				continue
			}
			if old, ok := seenAgents[step.Agent]; ok {
				if old != step.ProviderHash {
					return ErrDenied
				}
				continue
			}
			seenAgents[step.Agent] = step.ProviderHash
			if err := s.providers.FreezeWorkflowProvider(ctx, tx, j.ID, step.Agent, step.ProviderHash, 4096); err != nil {
				return err
			}
		}
		return nil
	}
	return visit(g.Root)
}

func (s *Service) compileGraph(ctx context.Context, user, workspace, pipelineID string) (frozenGraph, error) {
	var graph frozenGraph
	store := access.Store{DB: s.db}
	authority := s.providers
	if authority == nil {
		return graph, ErrDenied
	}
	leaves, nodes, bytes := 0, 0, 0
	seenBytes := map[string]bool{}
	path := map[string]bool{}
	targets := map[string]bool{}
	var visit func(string, int) (*graphRecipe, error)
	visit = func(recipeID string, depth int) (*graphRecipe, error) {
		if depth > 4 || path[recipeID] {
			return nil, ErrUnsupported
		}
		path[recipeID] = true
		defer delete(path, recipeID)
		r := &graphRecipe{ID: recipeID}
		if err := s.db.QueryRowContext(ctx, `SELECT definition_json,author_crew_id FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL AND status='active'`, recipeID, workspace).Scan(&r.Raw, &r.Crew); err != nil {
			return nil, ErrDenied
		}
		r.Hash = hash(r.Raw)
		if !seenBytes[recipeID] {
			bytes += len(r.Raw)
			seenBytes[recipeID] = true
		}
		if bytes > 256<<10 {
			return nil, ErrUnsupported
		}
		d, err := compileTyped(r.Raw, true)
		if err != nil {
			return nil, err
		}
		for _, step := range d.DSL.Steps {
			nodes++
			if nodes > 64 {
				return nil, ErrUnsupported
			}
			g := graphStep{ID: step.ID}
			if step.PipelineSlug != "" {
				var childID string
				if err = s.db.QueryRowContext(ctx, `SELECT id FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL AND status='active'`, workspace, step.PipelineSlug).Scan(&childID); err != nil {
					return nil, ErrDenied
				}
				g.Child, err = visit(childID, depth+1)
				if err != nil {
					return nil, err
				}
				childDef, err := compileTyped(g.Child.Raw, true)
				if err != nil {
					return nil, err
				}
				declared := map[string]bool{}
				for _, input := range childDef.DSL.Inputs {
					declared[input.Name] = true
					if input.Required && input.Default == nil {
						if _, ok := step.NestedInputs[input.Name]; !ok {
							return nil, ErrDenied
						}
					}
				}
				for key := range step.NestedInputs {
					if !declared[key] {
						return nil, ErrDenied
					}
				}
			} else {
				leaves++
				if leaves > 16 {
					return nil, ErrUnsupported
				}
				rows, err := s.db.QueryContext(ctx, `SELECT id,restricted_execution_profile FROM agents WHERE workspace_id=? AND slug=? AND deleted_at IS NULL AND (? IS NULL OR crew_id=?) LIMIT 2`, workspace, step.AgentSlug, r.Crew, r.Crew)
				if err != nil {
					return nil, ErrDenied
				}
				count := 0
				for rows.Next() {
					count++
					err = rows.Scan(&g.Agent, &g.Profile)
					if err != nil {
						break
					}
				}
				rowErr := rows.Err()
				rows.Close()
				if err != nil || rowErr != nil || count != 1 || (g.Profile != "responses_text" && g.Profile != "native_api_key") {
					return nil, ErrDenied
				}
				g.ProviderHash, err = authority.ProviderDelegationHash(ctx, user, workspace, g.Agent)
				if err != nil {
					return nil, ErrDenied
				}
				if graph.Agent == "" {
					graph.Agent = g.Agent
					graph.Profile = g.Profile
				}
				targets[g.Agent] = true
			}
			r.Steps = append(r.Steps, g)
		}
		return r, nil
	}
	var err error
	graph.Root, err = visit(pipelineID, 1)
	if err != nil || leaves == 0 {
		return frozenGraph{}, ErrUnsupported
	}
	for target := range targets {
		graph.Rights = append(graph.Rights, access.Right{Kind: "agent", ID: target, Operation: "run"})
		if target != graph.Agent {
			right := access.Right{Kind: "agent", ID: target, Operation: "delegate"}
			if store.Check(ctx, user, workspace, right) != nil {
				return frozenGraph{}, ErrDenied
			}
			graph.Rights = append(graph.Rights, right)
		}
	}
	// Sort via canonical JSON-independent rights ordering before hashing/pinning.
	for i := 0; i < len(graph.Rights); i++ {
		for j := i + 1; j < len(graph.Rights); j++ {
			if graph.Rights[j].ID+graph.Rights[j].Operation < graph.Rights[i].ID+graph.Rights[i].Operation {
				graph.Rights[i], graph.Rights[j] = graph.Rights[j], graph.Rights[i]
			}
		}
	}
	encoded, err := json.Marshal(graph)
	if err != nil || len(encoded) > 1<<20 {
		return frozenGraph{}, ErrUnsupported
	}
	return graph, nil
}
