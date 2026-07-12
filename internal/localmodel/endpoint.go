// Package localmodel is the single source of truth for the on-disk shape of an
// ENDPOINT_URL credential value (#961): a bare base URL, or a JSON object
// carrying the base URL plus optional auth token / custom headers for an
// authenticated OpenAI-compatible endpoint (Ollama-behind-proxy, LiteLLM).
//
// It is a dependency-free leaf package so both the server (internal/api) and
// the CLI (cmd/crewship, including the clionly build that must not pull server
// code) build/parse the value through the same code — no drifting struct tags.
package localmodel

import (
	"encoding/json"
	"fmt"
	"strings"
)

// EndpointValue is the JSON shape stored when an ENDPOINT_URL credential
// carries auth material. A value with neither apiKey nor headers is stored as a
// bare URL string instead (the #957 shape) so simple endpoints stay readable.
type EndpointValue struct {
	BaseURL string            `json:"baseURL"`
	APIKey  string            `json:"apiKey,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Build serialises a base URL plus optional auth token/headers into the stored
// credential value. With no auth it returns the bare, human-readable URL; with
// auth it returns the compact JSON object.
func Build(baseURL, apiKey string, headers map[string]string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if apiKey == "" && len(headers) == 0 {
		return baseURL, nil
	}
	raw, err := json.Marshal(EndpointValue{BaseURL: baseURL, APIKey: apiKey, Headers: headers})
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// Parse decodes a stored ENDPOINT_URL value into its base URL and optional auth
// material. A value beginning with "{" is parsed as the JSON object (and must
// carry a non-empty baseURL); anything else is a bare base URL with no auth.
func Parse(value string) (baseURL, apiKey string, headers map[string]string, err error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", "", nil, fmt.Errorf("endpoint value is empty")
	}
	if strings.HasPrefix(v, "{") {
		var ev EndpointValue
		if e := json.Unmarshal([]byte(v), &ev); e != nil {
			return "", "", nil, fmt.Errorf("endpoint value is not valid JSON: %w", e)
		}
		if strings.TrimSpace(ev.BaseURL) == "" {
			return "", "", nil, fmt.Errorf("endpoint JSON must include a non-empty baseURL")
		}
		return strings.TrimSpace(ev.BaseURL), ev.APIKey, ev.Headers, nil
	}
	return v, "", nil, nil
}
