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
	AgentID      string               `json:"agent_id"`
	AgentSlug    string               `json:"agent_slug"`
	CrewID       string               `json:"crew_id"`
	CrewSlug     string               `json:"crew_slug"`
	ContainerID  string               `json:"container_id"`
	CLIAdapter   string               `json:"cli_adapter"`
	SystemPrompt string               `json:"system_prompt"`
	ToolProfile  string               `json:"tool_profile"`
	Credentials  []credentialResponse `json:"credentials"`
	TimeoutSecs  int                  `json:"timeout_seconds"`
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

	return &ChatInfo{
		AgentID:      data.AgentID,
		AgentSlug:    data.AgentSlug,
		CrewID:       data.CrewID,
		CrewSlug:     data.CrewSlug,
		ContainerID:  data.ContainerID,
		CLIAdapter:   data.CLIAdapter,
		SystemPrompt: data.SystemPrompt,
		ToolProfile:  data.ToolProfile,
		Credentials:  creds,
		TimeoutSecs:  data.TimeoutSecs,
	}, nil
}
