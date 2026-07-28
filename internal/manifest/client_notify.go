package manifest

import (
	"context"
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Client calls for the notification and Composio surfaces.
//
// Kept out of client.go because these are the only calls whose bodies are
// built from secrets the manifest never stores — the value always arrives from
// the caller's CredentialSource, never from a parsed field.

// NotifyChannelResponse is the trimmed channel row the planner diffs against.
//
// The destination is deliberately absent: the list endpoint does not return
// it, and a manifest never needs it — identity is (type, provider) plus, for
// email, the address the manifest itself declared.
type NotifyChannelResponse struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Provider string   `json:"provider"`
	To       string   `json:"to"`
	URL      string   `json:"url"`
	Enabled  bool     `json:"enabled"`
	Scope    string   `json:"scope"`
	Cats     []string `json:"categories"`
}

// ListNotificationChannels returns the workspace's channels.
func (c *Client) ListNotificationChannels(ctx context.Context) ([]NotifyChannelResponse, error) {
	resp, err := c.api.Get(ctx, "/api/v1/notification-channels")
	if err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var body struct {
		Channels []NotifyChannelResponse `json:"channels"`
	}
	if err := decodeJSON(resp.Body, &body); err != nil {
		return nil, err
	}
	return body.Channels, nil
}

// CreateNotificationChannel posts a new channel.
func (c *Client) CreateNotificationChannel(ctx context.Context, body map[string]any) (*NotifyChannelResponse, error) {
	resp, err := c.api.Post(ctx, "/api/v1/notification-channels", body)
	if err != nil {
		return nil, fmt.Errorf("create notification channel: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil, err
	}
	var out NotifyChannelResponse
	if err := decodeJSON(resp.Body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PatchNotificationChannel updates an existing channel in place.
func (c *Client) PatchNotificationChannel(ctx context.Context, id string, body map[string]any) error {
	resp, err := c.api.Patch(ctx, "/api/v1/notification-channels/"+id, body)
	if err != nil {
		return fmt.Errorf("patch notification channel: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// DeleteNotificationChannel removes a channel the manifest no longer declares.
func (c *Client) DeleteNotificationChannel(ctx context.Context, id string) error {
	resp, err := c.api.Delete(ctx, "/api/v1/notification-channels/"+id)
	if err != nil {
		return fmt.Errorf("delete notification channel: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// AllowAgentOnChannel grants an agent permission to post to a channel.
func (c *Client) AllowAgentOnChannel(ctx context.Context, channelID, agentID string) error {
	resp, err := c.api.Post(ctx, "/api/v1/notification-channels/"+channelID+"/agents",
		map[string]any{"agent_id": agentID})
	if err != nil {
		return fmt.Errorf("pair agent to channel: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// FindAgentIDBySlug resolves an agent slug across the whole workspace.
//
// The cached per-crew lookup cannot serve this: a channel grant names an agent
// without naming its crew, which is right — an agent's permission to post
// somewhere has nothing to do with which crew it sits in.
func (c *Client) FindAgentIDBySlug(ctx context.Context, slug string) (string, error) {
	resp, err := c.api.Get(ctx, "/api/v1/agents")
	if err != nil {
		return "", fmt.Errorf("list agents: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return "", err
	}
	var rows []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	}
	if err := decodeJSON(resp.Body, &rows); err != nil {
		return "", err
	}
	for _, r := range rows {
		if r.Slug == slug {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("agent with slug %q not found in this workspace", slug)
}

// ComposioBinding is one agent-to-toolkit grant as the server reports it.
type ComposioBinding struct {
	Toolkit string `json:"toolkit"`
	Mode    string `json:"mode"`
	UserID  string `json:"user_id"`
}

// ListAgentToolkits returns an agent's current Composio grants.
//
// Read during planning so a grant that already matches reports as unchanged.
// Best-effort: an instance with no Composio key answers 4xx here, and that is
// "nothing granted", not a reason to fail a plan that is mostly about crews.
func (c *Client) ListAgentToolkits(ctx context.Context, agentID string) []ComposioBinding {
	resp, err := c.api.Get(ctx, "/api/v1/integrations/composio/agents/"+agentID+"/bind")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return nil
	}
	var body struct {
		Bindings []ComposioBinding `json:"bindings"`
	}
	if err := decodeJSON(resp.Body, &body); err != nil {
		return nil
	}
	return body.Bindings
}

// prefCell mirrors notifyroute.PrefCell on the wire.
type prefCell struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id"`
	State     string `json:"state"`
}

// RouteCategoriesToChannel sets the APPLYING USER's preferences so these
// categories deliver to this channel.
//
// PUT here is an upsert of the named cells, not a replacement of the whole
// matrix — a manifest declaring two categories must not silently mute
// everything else the person had configured.
func (c *Client) RouteCategoriesToChannel(ctx context.Context, channelID string, categories []string) error {
	cells := make([]prefCell, 0, len(categories))
	for _, cat := range categories {
		cells = append(cells, prefCell{Category: cat, ChannelID: channelID, State: "immediate"})
	}
	resp, err := c.api.Put(ctx, "/api/v1/me/notification-prefs", map[string]any{"cells": cells})
	if err != nil {
		return fmt.Errorf("set notification prefs: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// SetComposioAPIKey stores the workspace's Composio project key.
func (c *Client) SetComposioAPIKey(ctx context.Context, key string) error {
	resp, err := c.api.Put(ctx, "/api/v1/integrations/composio/settings",
		map[string]any{"api_key": key})
	if err != nil {
		return fmt.Errorf("set composio api key: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}

// BindAgentToolkit grants an agent access to a Composio toolkit.
//
// Applying a grant for a toolkit nobody has connected yet is legitimate — the
// account behind it is a per-user OAuth handshake no manifest can perform, and
// the grant takes effect the moment someone connects. So a "no connected
// account" refusal is not an apply failure.
func (c *Client) BindAgentToolkit(ctx context.Context, agentID string, body map[string]any) error {
	resp, err := c.api.Post(ctx, "/api/v1/integrations/composio/agents/"+agentID+"/bind", body)
	if err != nil {
		return fmt.Errorf("bind composio toolkit: %w", err)
	}
	defer resp.Body.Close()
	return cli.CheckError(resp)
}
