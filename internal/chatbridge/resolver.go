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

type sessionResolveResponse struct {
	AgentID      string               `json:"agent_id"`
	AgentSlug    string               `json:"agent_slug"`
	TeamID       string               `json:"team_id"`
	TeamSlug     string               `json:"team_slug"`
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

type CreateSessionRequest struct {
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
	OrgID     string `json:"org_id"`
	UserID    string `json:"user_id,omitempty"`
	Title     string `json:"title,omitempty"`
}

func (r *IPCResolver) CreateSession(ctx context.Context, req CreateSessionRequest) error {
	url := fmt.Sprintf("%s/api/v1/internal/sessions", r.baseURL)

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal create session request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("create session %s: %w", req.SessionID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		r.logger.Error("session create failed", "session_id", req.SessionID, "status", resp.StatusCode)
		return fmt.Errorf("session create returned %d", resp.StatusCode)
	}

	return nil
}

func (r *IPCResolver) ResolveSession(ctx context.Context, sessionID string) (*SessionInfo, error) {
	resolveURL := fmt.Sprintf("%s/api/v1/internal/sessions/%s/resolve", r.baseURL, url.PathEscape(sessionID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolveURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("X-Internal-Token", r.internalToken)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("resolve session %s: %w", sessionID, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		r.logger.Error("session resolve failed", "session_id", sessionID, "status", resp.StatusCode)
		return nil, fmt.Errorf("session resolve returned %d", resp.StatusCode)
	}

	var data sessionResolveResponse
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

	return &SessionInfo{
		AgentID:      data.AgentID,
		AgentSlug:    data.AgentSlug,
		TeamID:       data.TeamID,
		TeamSlug:     data.TeamSlug,
		ContainerID:  data.ContainerID,
		CLIAdapter:   data.CLIAdapter,
		SystemPrompt: data.SystemPrompt,
		ToolProfile:  data.ToolProfile,
		Credentials:  creds,
		TimeoutSecs:  data.TimeoutSecs,
	}, nil
}
