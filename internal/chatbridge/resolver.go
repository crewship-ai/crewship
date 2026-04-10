package chatbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ErrNoWebhookSecret is returned when an agent has no webhook secret configured.
var ErrNoWebhookSecret = errors.New("no webhook secret configured")

// IPCResolver implements ChatResolver by making HTTP calls to the internal API
// endpoints, authenticated with X-Internal-Token headers.
type IPCResolver struct {
	baseURL       string
	internalToken string
	httpClient    *http.Client
	logger        *slog.Logger
}

// NewIPCResolver creates an IPCResolver that calls the internal API at the given URL.
func NewIPCResolver(nextjsURL, internalToken string, logger *slog.Logger) *IPCResolver {
	return &IPCResolver{
		baseURL:       nextjsURL,
		internalToken: internalToken,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger: logger,
	}
}

type chatResolveResponse struct {
	AgentID        string               `json:"agent_id"`
	AgentSlug      string               `json:"agent_slug"`
	AgentRole      string               `json:"agent_role"`
	CrewID         string               `json:"crew_id"`
	CrewSlug       string               `json:"crew_slug"`
	ContainerID    string               `json:"container_id"`
	CLIAdapter     string               `json:"cli_adapter"`
	LLMModel       string               `json:"llm_model"`
	SystemPrompt   string               `json:"system_prompt"`
	ToolProfile    string               `json:"tool_profile"`
	Credentials    []credentialResponse `json:"credentials"`
	TimeoutSecs    int                  `json:"timeout_seconds"`
	WorkspaceID    string               `json:"workspace_id"`
	MemoryEnabled  bool                 `json:"memory_enabled"`
	CrewMembers    []crewMemberResponse `json:"crew_members"`
	AllCrews       []crewInfoResponse      `json:"all_crews,omitempty"`
	ActiveMissions []missionSummaryResponse `json:"active_missions,omitempty"`
	NetworkMode    string                  `json:"network_mode"`
	AllowedDomains []string                `json:"allowed_domains"`
	MemoryMB       int                     `json:"memory_mb"`
	CPUs           float64                 `json:"cpus"`
	TTLHours       int                     `json:"ttl_hours"`
	MCPServers         []mcpServerResponse `json:"mcp_servers,omitempty"`
	CrewMCPConfigJSON  string              `json:"crew_mcp_config_json"`
	AgentMCPConfigJSON string              `json:"agent_mcp_config_json"`
}

type mcpServerResponse struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	DisplayName string            `json:"display_name"`
	Transport   string            `json:"transport"`
	Endpoint    string            `json:"endpoint,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	CredToken    string            `json:"cred_token,omitempty"`
	CredType     string            `json:"cred_type,omitempty"`
	CredHeader   string            `json:"cred_header,omitempty"`
	EnvVarName   string            `json:"env_var_name,omitempty"`
}

type crewInfoResponse struct {
	ID      string               `json:"id"`
	Name    string               `json:"name"`
	Slug    string               `json:"slug"`
	Members []crewMemberResponse `json:"members"`
}

type missionSummaryResponse struct {
	ID       string `json:"id"`
	CrewSlug string `json:"crew_slug"`
	Title    string `json:"title"`
	Status   string `json:"status"`
}

type memberIntegrationResponse struct {
	Name       string   `json:"name"`
	ServerName string   `json:"server_name"`
	Tools      []string `json:"tools"`
}

type crewMemberResponse struct {
	ID           string                       `json:"id"`
	Name         string                       `json:"name"`
	Slug         string                       `json:"slug"`
	RoleTitle    string                       `json:"role_title"`
	Description  string                       `json:"description"`
	Status       string                       `json:"status"`
	ChatID       string                       `json:"chat_id,omitempty"`
	Integrations []memberIntegrationResponse  `json:"integrations,omitempty"`
}

type credentialResponse struct {
	ID       string `json:"id"`
	EnvVar   string `json:"env_var"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

// CreateChatRequest holds the parameters for creating a new chat session.
type CreateChatRequest struct {
	ChatID string `json:"chat_id"`
	AgentID   string `json:"agent_id"`
	WorkspaceID     string `json:"workspace_id"`
	UserID    string `json:"user_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

// CreateChat creates a new chat session via the internal API.
func (r *IPCResolver) CreateChat(ctx context.Context, req CreateChatRequest) error {
	url := fmt.Sprintf("%s/api/v1/internal/chats", r.baseURL)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal create chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("create chat %s: %w", req.ChatID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		r.logger.Error("chat create failed", "chat_id", req.ChatID, "status", resp.StatusCode)
		return fmt.Errorf("chat create returned %d", resp.StatusCode)
	}

	return nil
}

// CreateRun records a new agent run via the internal API.
func (r *IPCResolver) CreateRun(ctx context.Context, runID, agentID, chatID, workspaceID, triggerType string, metadata map[string]interface{}) error {
	reqURL := fmt.Sprintf("%s/api/v1/internal/runs", r.baseURL)
	payload := map[string]interface{}{
		"id": runID, "agent_id": agentID, "chat_id": chatID,
		"workspace_id": workspaceID, "trigger_type": triggerType,
	}
	if metadata != nil {
		payload["metadata"] = metadata
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("create run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create run: server returned %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// UpdateRun updates a run's status, exit code, and metadata via the internal API.
func (r *IPCResolver) UpdateRun(ctx context.Context, runID, status string, exitCode *int, errorMsg *string, metadata map[string]interface{}) error {
	reqURL := fmt.Sprintf("%s/api/v1/internal/runs/%s", r.baseURL, url.PathEscape(runID))
	payload := map[string]interface{}{"status": status}
	if exitCode != nil {
		payload["exit_code"] = *exitCode
	}
	if errorMsg != nil {
		payload["error_message"] = *errorMsg
	}
	if metadata != nil {
		payload["metadata"] = metadata
	}
	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("update run: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update run: server returned %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

// IncrementMessageCount increments the message count for a chat session.
func (r *IPCResolver) IncrementMessageCount(ctx context.Context, chatID string, delta int) error {
	reqURL := fmt.Sprintf("%s/api/v1/internal/chats/%s/message-count", r.baseURL, url.PathEscape(chatID))
	body, _ := json.Marshal(map[string]int{"delta": delta})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("increment message count: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("increment message count: server returned %d", resp.StatusCode)
	}
	return nil
}

// UpdateChatTitle updates the display title of a chat session.
func (r *IPCResolver) UpdateChatTitle(ctx context.Context, chatID, title string) error {
	reqURL := fmt.Sprintf("%s/api/v1/internal/chats/%s/title", r.baseURL, url.PathEscape(chatID))
	body, _ := json.Marshal(map[string]string{"title": title})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPatch, reqURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("update chat title: create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)
	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("update chat title: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("update chat title: server returned %d", resp.StatusCode)
	}
	return nil
}

// ResolveChat resolves a chat ID to the full agent configuration via the internal API.
func (r *IPCResolver) ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error) {
	resolveURL := fmt.Sprintf("%s/api/v1/internal/chats/%s/resolve", r.baseURL, url.PathEscape(chatID))
	return r.resolve(ctx, resolveURL)
}

// ResolveAgent resolves an agent ID to its configuration via the internal API.
func (r *IPCResolver) ResolveAgent(ctx context.Context, agentID string) (*ChatInfo, error) {
	resolveURL := fmt.Sprintf("%s/api/v1/internal/agents/%s/resolve", r.baseURL, url.PathEscape(agentID))
	return r.resolve(ctx, resolveURL)
}

// GetWebhookSecret retrieves the webhook secret for an agent via the internal API.
func (r *IPCResolver) GetWebhookSecret(ctx context.Context, agentID string) (string, error) {
	u := fmt.Sprintf("%s/api/v1/internal/agents/%s/webhook-secret", r.baseURL, url.PathEscape(agentID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get webhook secret returned %d", resp.StatusCode)
	}

	var data struct {
		Secret string `json:"webhook_secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.Secret == "" {
		return "", fmt.Errorf("%w: agent %s", ErrNoWebhookSecret, agentID)
	}
	return data.Secret, nil
}

func (r *IPCResolver) resolve(ctx context.Context, resolveURL string) (*ChatInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve agent: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		r.logger.Error("resolve failed", "url", resolveURL, "status", resp.StatusCode)
		return nil, fmt.Errorf("resolve returned %d", resp.StatusCode)
	}

	var data chatResolveResponse
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("decode resolve response: %w", err)
	}

	creds := make([]orchestrator.Credential, len(data.Credentials))
	for i, c := range data.Credentials {
		creds[i] = orchestrator.Credential{
			ID:         c.ID,
			EnvVarName: c.EnvVar,
			PlainValue: c.Value,
			Priority:   c.Priority,
			Type:       c.Type,
		}
	}

	var crewMembers []orchestrator.CrewMember
	for _, m := range data.CrewMembers {
		cm := orchestrator.CrewMember{
			ID:          m.ID,
			Name:        m.Name,
			Slug:        m.Slug,
			RoleTitle:   m.RoleTitle,
			Description: m.Description,
			Status:      m.Status,
			ChatID:      m.ChatID,
		}
		for _, ig := range m.Integrations {
			cm.Integrations = append(cm.Integrations, orchestrator.MemberIntegration{
				Name:       ig.Name,
				ServerName: ig.ServerName,
				Tools:      ig.Tools,
			})
		}
		crewMembers = append(crewMembers, cm)
	}

	networkMode := data.NetworkMode
	if networkMode == "" {
		networkMode = "free"
	}
	allowedDomains := data.AllowedDomains
	if allowedDomains == nil {
		allowedDomains = []string{}
	}

	// Convert all_crews for COORDINATOR agents
	var allCrews []orchestrator.CrewInfo
	for _, c := range data.AllCrews {
		ci := orchestrator.CrewInfo{
			ID:   c.ID,
			Name: c.Name,
			Slug: c.Slug,
		}
		for _, m := range c.Members {
			cm := orchestrator.CrewMember{
				ID:          m.ID,
				Name:        m.Name,
				Slug:        m.Slug,
				RoleTitle:   m.RoleTitle,
				Description: m.Description,
				Status:      m.Status,
				ChatID:      m.ChatID,
			}
			for _, ig := range m.Integrations {
				cm.Integrations = append(cm.Integrations, orchestrator.MemberIntegration{
					Name:       ig.Name,
					ServerName: ig.ServerName,
					Tools:      ig.Tools,
				})
			}
			ci.Members = append(ci.Members, cm)
		}
		allCrews = append(allCrews, ci)
	}

	var activeMissions []orchestrator.MissionSummary
	for _, m := range data.ActiveMissions {
		activeMissions = append(activeMissions, orchestrator.MissionSummary{
			ID:       m.ID,
			CrewSlug: m.CrewSlug,
			Title:    m.Title,
			Status:   m.Status,
		})
	}

	var mcpServers []orchestrator.MCPServerConfig
	for _, s := range data.MCPServers {
		cfg := orchestrator.MCPServerConfig{
			ID: s.ID, Name: s.Name, DisplayName: s.DisplayName,
			Transport: s.Transport, Endpoint: s.Endpoint,
			Command: s.Command, Args: s.Args, Env: s.Env,
		}
		if s.CredToken != "" {
			header := s.CredHeader
			// For stdio servers, env_var_name takes precedence over header
			if s.Transport == "stdio" && s.EnvVarName != "" {
				header = s.EnvVarName
			}
			cfg.Credential = &orchestrator.MCPCredential{
				PlainValue: s.CredToken,
				Type:       s.CredType,
				Header:     header,
			}
		}
		mcpServers = append(mcpServers, cfg)
	}

	return &ChatInfo{
		AgentID:        data.AgentID,
		AgentSlug:      data.AgentSlug,
		AgentRole:      data.AgentRole,
		CrewID:         data.CrewID,
		CrewSlug:       data.CrewSlug,
		ContainerID:    data.ContainerID,
		CLIAdapter:     data.CLIAdapter,
		LLMModel:       data.LLMModel,
		SystemPrompt:   data.SystemPrompt,
		ToolProfile:    data.ToolProfile,
		Credentials:    creds,
		TimeoutSecs:    data.TimeoutSecs,
		WorkspaceID:    data.WorkspaceID,
		MemoryEnabled:  data.MemoryEnabled,
		CrewMembers:    crewMembers,
		AllCrews:       allCrews,
		ActiveMissions: activeMissions,
		NetworkMode:    networkMode,
		AllowedDomains: allowedDomains,
		MemoryMB:       data.MemoryMB,
		CPUs:           data.CPUs,
		TTLHours:       data.TTLHours,
		MCPServers:         mcpServers,
		CrewMCPConfigJSON:  data.CrewMCPConfigJSON,
		AgentMCPConfigJSON: data.AgentMCPConfigJSON,
	}, nil
}
