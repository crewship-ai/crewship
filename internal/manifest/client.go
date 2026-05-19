package manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// APIClient is the subset of *cli.Client that internal/manifest needs.
// Defined as an interface so tests can swap in a fake without spinning
// up a real HTTP server. The real implementation is *cli.Client; the
// methods listed here match its signatures exactly.
type APIClient interface {
	Get(path string) (*http.Response, error)
	Post(path string, body any) (*http.Response, error)
	Patch(path string, body any) (*http.Response, error)
	Delete(path string) (*http.Response, error)
	GetWorkspaceID() string
}

// Client wraps APIClient with manifest-specific operations. Each
// method maps to a single REST call against the existing Crewship
// API — no bulk endpoints, no manifest-aware server logic. The
// design choice mirrors `kubectl apply`: the server stays dumb and
// every action is a regular resource mutation, so RBAC, audit, and
// WebSocket fanout work the same as for an interactive user.
//
// The Client also memoises list endpoints between calls because the
// plan and apply phases each look up "does X exist?" for every
// resource — without a cache, a workspace bundle with 10 crews and
// 30 credentials issues 50+ identical list-credentials requests.
// Cache entries are flushed by the *Reset family of methods that
// mutating operations call after they alter the underlying state.
type Client struct {
	api APIClient

	crewsCache   []CrewResponse
	crewsLoaded  bool
	credsCache   []CredentialResponse
	credsLoaded  bool
	skillsCache  []SkillResponse
	skillsLoaded bool
	agentsByCrew map[string][]AgentResponse
	mcpsByCrew   map[string][]MCPServerResponse
}

// NewClient returns a Client bound to api. If api is a *cli.Client
// the workspace_id query-param injection runs automatically; tests
// can pass a stub APIClient that returns whatever they like.
func NewClient(api APIClient) *Client {
	return &Client{
		api:          api,
		agentsByCrew: map[string][]AgentResponse{},
		mcpsByCrew:   map[string][]MCPServerResponse{},
	}
}

// invalidateCrews drops the cached crew list — called after every
// crew create/update/delete so the next lookup re-fetches.
func (c *Client) invalidateCrews()               { c.crewsLoaded = false; c.crewsCache = nil }
func (c *Client) invalidateCreds()               { c.credsLoaded = false; c.credsCache = nil }
func (c *Client) invalidateSkills()              { c.skillsLoaded = false; c.skillsCache = nil }
func (c *Client) invalidateAgents(crewID string) { delete(c.agentsByCrew, crewID) }
func (c *Client) invalidateMCPs(crewID string)   { delete(c.mcpsByCrew, crewID) }

// ---------- crews ----------

type CrewResponse struct {
	ID                 string   `json:"id"`
	WorkspaceID        string   `json:"workspace_id"`
	Name               string   `json:"name"`
	Slug               string   `json:"slug"`
	Description        *string  `json:"description"`
	Color              *string  `json:"color"`
	Icon               *string  `json:"icon"`
	ContainerMemoryMB  *int     `json:"container_memory_mb"`
	ContainerCPUs      *float64 `json:"container_cpus"`
	ContainerTTLHours  *int     `json:"container_ttl_hours"`
	NetworkMode        *string  `json:"network_mode"`
	AllowedDomains     []string `json:"allowed_domains"`
	RuntimeImage       *string  `json:"runtime_image"`
	DevcontainerConfig *string  `json:"devcontainer_config"`
	MiseConfig         *string  `json:"mise_config"`
	ServicesJSON       *string  `json:"services_json"`
}

// ListCrews returns every crew in the workspace, caching the result
// between calls. The cache is invalidated by any mutation method on
// this Client.
func (c *Client) ListCrews(ctx context.Context) ([]CrewResponse, error) {
	if c.crewsLoaded {
		return c.crewsCache, nil
	}
	body, err := c.fetchBody("/api/v1/crews")
	if err != nil {
		return nil, fmt.Errorf("list crews: %w", err)
	}
	var crews []CrewResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &crews); err != nil {
			return nil, fmt.Errorf("decode crews: %w", err)
		}
	}
	c.crewsCache = crews
	c.crewsLoaded = true
	return crews, nil
}

// FindCrewBySlug returns the crew with the given slug, or nil if it
// doesn't exist. Errors are returned for network/server failures
// only — a 404 from the API maps to (nil, nil).
func (c *Client) FindCrewBySlug(ctx context.Context, slug string) (*CrewResponse, error) {
	crews, err := c.ListCrews(ctx)
	if err != nil {
		return nil, err
	}
	for i := range crews {
		if crews[i].Slug == slug {
			return &crews[i], nil
		}
	}
	return nil, nil
}

func (c *Client) CreateCrew(ctx context.Context, body map[string]any) (*CrewResponse, error) {
	resp, err := c.api.Post("/api/v1/crews", body)
	if err != nil {
		return nil, fmt.Errorf("create crew: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var crew CrewResponse
	if err := decodeJSON(resp.Body, &crew); err != nil {
		return nil, fmt.Errorf("decode crew: %w", err)
	}
	c.invalidateCrews()
	return &crew, nil
}

func (c *Client) UpdateCrew(ctx context.Context, crewID string, body map[string]any) (*CrewResponse, error) {
	resp, err := c.api.Patch("/api/v1/crews/"+crewID, body)
	if err != nil {
		return nil, fmt.Errorf("update crew: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var crew CrewResponse
	if err := decodeJSON(resp.Body, &crew); err != nil {
		return nil, fmt.Errorf("decode crew: %w", err)
	}
	c.invalidateCrews()
	return &crew, nil
}

func (c *Client) DeleteCrew(ctx context.Context, crewID string) error {
	resp, err := c.api.Delete("/api/v1/crews/" + crewID)
	if err != nil {
		return fmt.Errorf("delete crew: %w", err)
	}
	resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	c.invalidateCrews()
	c.invalidateAgents(crewID)
	c.invalidateMCPs(crewID)
	return nil
}

// ---------- agents ----------

type AgentResponse struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	CrewID         *string `json:"crew_id"`
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	AgentRole      string  `json:"agent_role"`
	CLIAdapter     string  `json:"cli_adapter"`
	SystemPrompt   *string `json:"system_prompt"`
	LLMProvider    *string `json:"llm_provider"`
	LLMModel       *string `json:"llm_model"`
	ToolProfile    string  `json:"tool_profile"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	MemoryEnabled  bool    `json:"memory_enabled"`
	RoleTitle      *string `json:"role_title"`
}

func (c *Client) ListAgentsByCrew(ctx context.Context, crewID string) ([]AgentResponse, error) {
	if cached, ok := c.agentsByCrew[crewID]; ok {
		return cached, nil
	}
	body, err := c.fetchBody("/api/v1/agents?crew_id=" + crewID)
	if err != nil {
		return nil, err
	}
	var agents []AgentResponse
	if len(body) > 0 {
		if err := json.Unmarshal(body, &agents); err != nil {
			return nil, fmt.Errorf("decode agents: %w", err)
		}
	}
	c.agentsByCrew[crewID] = agents
	return agents, nil
}

func (c *Client) CreateAgent(ctx context.Context, body map[string]any) (*AgentResponse, error) {
	resp, err := c.api.Post("/api/v1/agents", body)
	if err != nil {
		return nil, fmt.Errorf("create agent: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var agent AgentResponse
	if err := decodeJSON(resp.Body, &agent); err != nil {
		return nil, err
	}
	if agent.CrewID != nil {
		c.invalidateAgents(*agent.CrewID)
	}
	return &agent, nil
}

func (c *Client) UpdateAgent(ctx context.Context, agentID string, body map[string]any) (*AgentResponse, error) {
	resp, err := c.api.Patch("/api/v1/agents/"+agentID, body)
	if err != nil {
		return nil, fmt.Errorf("update agent: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var agent AgentResponse
	if err := decodeJSON(resp.Body, &agent); err != nil {
		return nil, err
	}
	if agent.CrewID != nil {
		c.invalidateAgents(*agent.CrewID)
	}
	return &agent, nil
}

// DeleteAgent removes an agent from the workspace. Used by sync mode
// to drop agents that the manifest no longer declares.
func (c *Client) DeleteAgent(ctx context.Context, agentID, crewID string) error {
	resp, err := c.api.Delete("/api/v1/agents/" + agentID)
	if err != nil {
		return fmt.Errorf("delete agent: %w", err)
	}
	resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	c.invalidateAgents(crewID)
	return nil
}

// ---------- skills ----------

type SkillResponse struct {
	ID          string  `json:"id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Description *string `json:"description"`
	Version     *string `json:"version"`
	Created     bool    `json:"created"`
	SkillID     string  `json:"skill_id"` // mirrors import response shape
}

// ImportSkill upserts a skill by slug. Inline content is sent in
// `content`, URL refs in `url`; the server's /skills/import handler
// already supports both shapes. The response indicates whether the
// row was newly created or updated (the API uses different field
// names depending on path — we normalise here).
func (c *Client) ImportSkill(ctx context.Context, body map[string]any) (*SkillResponse, error) {
	wsID := c.api.GetWorkspaceID()
	if wsID == "" {
		return nil, errors.New("workspace_id is required for skill import")
	}
	resp, err := c.api.Post("/api/v1/workspaces/"+wsID+"/skills/import", body)
	if err != nil {
		return nil, fmt.Errorf("import skill: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var sr SkillResponse
	if err := decodeJSON(resp.Body, &sr); err != nil {
		return nil, err
	}
	// The import endpoint returns either {id, ...} or
	// {skill_id, ...} depending on caller. Normalise.
	if sr.ID == "" && sr.SkillID != "" {
		sr.ID = sr.SkillID
	}
	c.invalidateSkills()
	return &sr, nil
}

func (c *Client) ListSkills(ctx context.Context) ([]SkillResponse, error) {
	wsID := c.api.GetWorkspaceID()
	body, err := c.fetchBody("/api/v1/workspaces/" + wsID + "/skills")
	if err != nil {
		// The workspace-scoped path is what the router registers;
		// /api/v1/skills doesn't exist as of v94 but earlier
		// branches did register both. Fall back so existing
		// downstreams keep working without an API version bump.
		alt, altErr := c.fetchBody("/api/v1/skills")
		if altErr != nil {
			return nil, err
		}
		body = alt
	}
	return decodeSkillsList(body)
}

// decodeSkillsList accepts either a flat array or a wrapped
// {skills: [...]} object, the two shapes the API has shipped at
// different points. Bytes are decoded twice (try flat, fall back to
// wrapped) — that's only safe when we hold the raw bytes, hence the
// fetchBody helper instead of streaming the response.
func decodeSkillsList(body []byte) ([]SkillResponse, error) {
	if len(body) == 0 {
		return nil, nil
	}
	var flat []SkillResponse
	if err := json.Unmarshal(body, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Skills []SkillResponse `json:"skills"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		return wrapped.Skills, nil
	}
	return nil, fmt.Errorf("decode skills list: unknown shape (first bytes: %q)", firstBytes(body, 80))
}

// AddSkillToAgent attaches a skill to an agent. Idempotent on the
// server: a duplicate POST returns 409, which we swallow because the
// desired state ("skill linked") is achieved either way.
func (c *Client) AddSkillToAgent(ctx context.Context, agentID, skillID string) error {
	resp, err := c.api.Post("/api/v1/agents/"+agentID+"/skills", map[string]any{
		"skill_id": skillID,
	})
	if err != nil {
		return fmt.Errorf("add skill to agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil // already linked
	}
	return cli.CheckError(resp)
}

// RemoveSkillFromAgent unlinks a skill from an agent. Used by sync
// mode to drop agent_skills rows the manifest no longer declares.
func (c *Client) RemoveSkillFromAgent(ctx context.Context, agentID, skillID string) error {
	resp, err := c.api.Delete("/api/v1/agents/" + agentID + "/skills/" + skillID)
	if err != nil {
		return fmt.Errorf("remove skill from agent: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // already gone
	}
	return cli.CheckError(resp)
}

// AgentSkillBinding mirrors what GET /api/v1/agents/{id}/skills
// returns: the agent_skills join row plus the underlying skill
// snapshot. Sync mode reads these to compute the delete-set.
type AgentSkillBinding struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	SkillID string `json:"skill_id"`
	Skill   struct {
		Slug string `json:"slug"`
	} `json:"skill"`
}

func (c *Client) ListAgentSkills(ctx context.Context, agentID string) ([]AgentSkillBinding, error) {
	body, err := c.fetchBody("/api/v1/agents/" + agentID + "/skills")
	if err != nil {
		return nil, err
	}
	var bindings []AgentSkillBinding
	if len(body) > 0 {
		if err := json.Unmarshal(body, &bindings); err != nil {
			return nil, fmt.Errorf("decode agent skills: %w", err)
		}
	}
	return bindings, nil
}

// AgentCredentialBinding mirrors what GET /api/v1/agents/{id}/credentials
// returns.
type AgentCredentialBinding struct {
	ID           string `json:"id"`
	AgentID      string `json:"agent_id"`
	CredentialID string `json:"credential_id"`
	CredName     string `json:"credential_name"`
	EnvVarName   string `json:"env_var_name"`
}

func (c *Client) ListAgentCredentials(ctx context.Context, agentID string) ([]AgentCredentialBinding, error) {
	body, err := c.fetchBody("/api/v1/agents/" + agentID + "/credentials")
	if err != nil {
		return nil, err
	}
	var bindings []AgentCredentialBinding
	if len(body) > 0 {
		if err := json.Unmarshal(body, &bindings); err != nil {
			return nil, fmt.Errorf("decode agent credentials: %w", err)
		}
	}
	return bindings, nil
}

// RemoveCredentialFromAgent unlinks a credential binding (by the
// agent_credentials row id) from an agent.
func (c *Client) RemoveCredentialFromAgent(ctx context.Context, agentID, assignmentID string) error {
	resp, err := c.api.Delete("/api/v1/agents/" + agentID + "/credentials/" + assignmentID)
	if err != nil {
		return fmt.Errorf("remove credential from agent: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return cli.CheckError(resp)
}

// ---------- credentials ----------

type CredentialResponse struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Scope    string `json:"scope"`
}

// ListCredentials returns every credential in the workspace,
// cached between calls. Handles both flat-array and {credentials:
// [...]} response shapes the API has used at different points.
func (c *Client) ListCredentials(ctx context.Context) ([]CredentialResponse, error) {
	if c.credsLoaded {
		return c.credsCache, nil
	}
	body, err := c.fetchBody("/api/v1/credentials")
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		c.credsCache = nil
		c.credsLoaded = true
		return nil, nil
	}
	var flat []CredentialResponse
	if err := json.Unmarshal(body, &flat); err == nil {
		c.credsCache = flat
		c.credsLoaded = true
		return flat, nil
	}
	var wrapped struct {
		Credentials []CredentialResponse `json:"credentials"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		c.credsCache = wrapped.Credentials
		c.credsLoaded = true
		return wrapped.Credentials, nil
	}
	return nil, fmt.Errorf("decode credentials: unknown shape (first bytes: %q)", firstBytes(body, 80))
}

func (c *Client) FindCredentialByName(ctx context.Context, name string) (*CredentialResponse, error) {
	creds, err := c.ListCredentials(ctx)
	if err != nil {
		return nil, err
	}
	for i := range creds {
		if creds[i].Name == name {
			return &creds[i], nil
		}
	}
	return nil, nil
}

// CreateCredential creates a credential. body must include either
// a real value or "pending": true for a slot. The handler enforces
// the value/pending invariant.
func (c *Client) CreateCredential(ctx context.Context, body map[string]any) (*CredentialResponse, error) {
	resp, err := c.api.Post("/api/v1/credentials", body)
	if err != nil {
		return nil, fmt.Errorf("create credential: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var cred CredentialResponse
	if err := decodeJSON(resp.Body, &cred); err != nil {
		return nil, err
	}
	c.invalidateCreds()
	return &cred, nil
}

// LinkCredentialToAgent associates an existing credential with an
// agent under a specific env var name. Idempotent (409 = noop).
func (c *Client) LinkCredentialToAgent(ctx context.Context, agentID, credentialID, envVarName string) error {
	resp, err := c.api.Post("/api/v1/agents/"+agentID+"/credentials", map[string]any{
		"credential_id": credentialID,
		"env_var_name":  envVarName,
	})
	if err != nil {
		return fmt.Errorf("link credential to agent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	return cli.CheckError(resp)
}

// ---------- MCP servers (crew integrations) ----------

type MCPServerResponse struct {
	ID          string  `json:"id"`
	CrewID      string  `json:"crew_id"`
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Transport   string  `json:"transport"`
	Endpoint    *string `json:"endpoint"`
	Command     *string `json:"command"`
	Enabled     bool    `json:"enabled"`
}

func (c *Client) ListCrewIntegrations(ctx context.Context, crewID string) ([]MCPServerResponse, error) {
	body, err := c.fetchBody("/api/v1/crews/" + crewID + "/integrations")
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var flat []MCPServerResponse
	if err := json.Unmarshal(body, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Integrations []MCPServerResponse `json:"integrations"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		return wrapped.Integrations, nil
	}
	return nil, fmt.Errorf("decode crew integrations: unknown shape (first bytes: %q)", firstBytes(body, 80))
}

// fetchBody issues a GET, buffers the body so it can be decoded into
// multiple candidate shapes, and returns the bytes. Centralises the
// 2xx check and body-close discipline.
func (c *Client) fetchBody(path string) ([]byte, error) {
	resp, err := c.api.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// fetchBodyCtx is the cancellation-aware variant of fetchBody used
// by export paths that already carry the caller's context. When
// the underlying API client supports WithContext (i.e. *cli.Client),
// the request honours the ctx's deadline/cancellation. Tests that
// inject a bare APIClient fall back to fetchBody — losing
// cancellation is a known limit of the test fake, not a production
// path.
func (c *Client) fetchBodyCtx(ctx context.Context, path string) ([]byte, error) {
	type contextual interface {
		WithContext(context.Context) *cli.Client
	}
	if cc, ok := c.api.(contextual); ok && ctx != nil {
		return fetchBodyVia(cc.WithContext(ctx), path)
	}
	return c.fetchBody(path)
}

// fetchBodyVia is the same logic as fetchBody but against a caller-
// supplied client (used by fetchBodyCtx when it swaps in a
// ctx-bound copy). Kept separate to avoid threading a generic
// "client interface" type through every call site.
func fetchBodyVia(client *cli.Client, path string) ([]byte, error) {
	resp, err := client.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// fetchSkillContent returns the raw SKILL.md body for a skill by ID.
// Returns "" if the skill doesn't exist or has no content (the row
// can be a metadata-only stub for skills that live in OCI images).
func (c *Client) fetchSkillContent(id string) string {
	body, err := c.fetchBody("/api/v1/skills/" + id)
	if err != nil || len(body) == 0 {
		return ""
	}
	var detail struct {
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(body, &detail); err != nil {
		return ""
	}
	if detail.Content == nil {
		return ""
	}
	return *detail.Content
}

func firstBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}

func (c *Client) CreateCrewIntegration(ctx context.Context, crewID string, body map[string]any) error {
	resp, err := c.api.Post("/api/v1/crews/"+crewID+"/integrations", body)
	if err != nil {
		return fmt.Errorf("create crew integration: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		// Server enforces UNIQUE(crew_id, name); a duplicate name is
		// an authorial mistake (already exists with same shape), not
		// an apply-time failure. Surface as an error so the caller
		// sees the conflict and can decide.
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("mcp server already exists on crew: %s", strings.TrimSpace(string(body)))
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	c.invalidateMCPs(crewID)
	return nil
}

// DeleteCrewIntegration removes an MCP server from a crew. Used by
// sync mode to drop integrations the manifest no longer declares.
func (c *Client) DeleteCrewIntegration(ctx context.Context, crewID, integrationID string) error {
	resp, err := c.api.Delete("/api/v1/crews/" + crewID + "/integrations/" + integrationID)
	if err != nil {
		return fmt.Errorf("delete crew integration: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	c.invalidateMCPs(crewID)
	return nil
}

// ---------- internal helpers ----------

func decodeJSON(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, 10<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}
