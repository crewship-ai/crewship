package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// CrewCapabilities mirrors GET /api/v1/crews/{crewId}/capabilities — the
// one-shot authoring dump an LLM needs to write a valid routine DSL: the DSL
// schema, the crew's container capabilities, connected integrations with their
// enabled tool names, agent slugs, and the runtimes it can actually use.
type CrewCapabilities struct {
	CrewID    string `json:"crew_id"`
	CrewSlug  string `json:"crew_slug"`
	Container struct {
		Datastores []struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Host string `json:"host"`
			Port string `json:"port"`
		} `json:"datastores"`
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	} `json:"container"`
	Integrations []struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"display_name"`
		Tools       []string `json:"tools"`
	} `json:"integrations"`
	Agents []struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	} `json:"agents"`
	Runtimes struct {
		Code struct {
			Wired           []string `json:"wired"`
			ReservedUnwired []string `json:"reserved_unwired"`
		} `json:"code"`
		ScriptInterpreters map[string]string `json:"script_interpreters"`
	} `json:"runtimes"`
	Schema json.RawMessage `json:"schema"`
}

// CrewCapabilities fetches the authoring capability bundle for a crew.
func (c *Client) CrewCapabilities(ctx context.Context, crewID string) (*CrewCapabilities, error) {
	if strings.TrimSpace(crewID) == "" {
		return nil, errors.New("crew id required")
	}
	resp, err := c.WithContext(ctx).Get("/api/v1/crews/" + url.PathEscape(crewID) + "/capabilities")
	if err != nil {
		return nil, fmt.Errorf("get crew capabilities %q: %w", crewID, err)
	}
	if err := CheckError(resp); err != nil {
		return nil, fmt.Errorf("get crew capabilities %q: %w", crewID, err)
	}
	var out CrewCapabilities
	if err := ReadJSON(resp, &out); err != nil {
		return nil, fmt.Errorf("decode crew capabilities %q: %w", crewID, err)
	}
	return &out, nil
}
