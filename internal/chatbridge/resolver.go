package chatbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type IPCResolver struct {
	baseURL       string
	internalToken string
	httpClient    *http.Client
	logger        *slog.Logger
}

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
	AgentID       string               `json:"agent_id"`
	AgentSlug     string               `json:"agent_slug"`
	AgentRole     string               `json:"agent_role"`
	CrewID        string               `json:"crew_id"`
	CrewSlug      string               `json:"crew_slug"`
	ContainerID   string               `json:"container_id"`
	CLIAdapter    string               `json:"cli_adapter"`
	LLMModel      string               `json:"llm_model"`
	SystemPrompt  string               `json:"system_prompt"`
	ToolProfile   string               `json:"tool_profile"`
	Credentials   []credentialResponse `json:"credentials"`
	TimeoutSecs   int                  `json:"timeout_seconds"`
	WorkspaceID   string               `json:"workspace_id"`
	MemoryEnabled bool                 `json:"memory_enabled"`
	CrewMembers   []crewMemberResponse `json:"crew_members"`
}

type crewMemberResponse struct {
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	RoleTitle   string `json:"role_title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type credentialResponse struct {
	ID       string `json:"id"`
	EnvVar   string `json:"env_var"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Type     string `json:"type"`
}

type CreateChatRequest struct {
	ChatID string `json:"chat_id"`
	AgentID   string `json:"agent_id"`
	WorkspaceID     string `json:"workspace_id"`
	UserID    string `json:"user_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

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

func (r *IPCResolver) ResolveChat(ctx context.Context, chatID string) (*ChatInfo, error) {
	resolveURL := fmt.Sprintf("%s/api/v1/internal/chats/%s/resolve", r.baseURL, url.PathEscape(chatID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve chat %s: %w", chatID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		r.logger.Error("chat resolve failed", "chat_id", chatID, "status", resp.StatusCode)
		return nil, fmt.Errorf("chat resolve returned %d", resp.StatusCode)
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
		crewMembers = append(crewMembers, orchestrator.CrewMember{
			Name:        m.Name,
			Slug:        m.Slug,
			RoleTitle:   m.RoleTitle,
			Description: m.Description,
			Status:      m.Status,
		})
	}

	return &ChatInfo{
		AgentID:       data.AgentID,
		AgentSlug:     data.AgentSlug,
		AgentRole:     data.AgentRole,
		CrewID:        data.CrewID,
		CrewSlug:      data.CrewSlug,
		ContainerID:   data.ContainerID,
		CLIAdapter:    data.CLIAdapter,
		LLMModel:      data.LLMModel,
		SystemPrompt:  data.SystemPrompt,
		ToolProfile:   data.ToolProfile,
		Credentials:   creds,
		TimeoutSecs:   data.TimeoutSecs,
		WorkspaceID:   data.WorkspaceID,
		MemoryEnabled: data.MemoryEnabled,
		CrewMembers:   crewMembers,
	}, nil
}
