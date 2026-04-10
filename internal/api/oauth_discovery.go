package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAuthServerMetadata holds discovered OAuth server configuration (RFC 8414).
type OAuthServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
}

// ProtectedResourceMetadata holds OAuth protected resource info (RFC 9728).
type ProtectedResourceMetadata struct {
	Resource                string   `json:"resource"`
	AuthorizationServers    []string `json:"authorization_servers,omitempty"`
	ScopesSupported         []string `json:"scopes_supported,omitempty"`
}

// DiscoveredOAuth holds the result of OAuth metadata discovery for an MCP server.
type DiscoveredOAuth struct {
	AuthURL              string `json:"auth_url"`
	TokenURL             string `json:"token_url"`
	RegistrationEndpoint string `json:"registration_endpoint,omitempty"`
	Scopes               string `json:"scopes,omitempty"`
	SupportsPKCE         bool   `json:"supports_pkce"`
	SupportsDCR          bool   `json:"supports_dcr"`
}

var discoveryClient = &http.Client{
	Timeout:   10 * time.Second,
	Transport: ssrfSafeTransport(),
}

// discoverOAuthFromMCPURL tries to discover OAuth metadata for an MCP server URL.
//
// Discovery chain (per MCP spec):
// 1. GET {origin}/.well-known/oauth-protected-resource → find authorization_servers[0]
// 2. GET {auth_server}/.well-known/oauth-authorization-server → get endpoints
// 3. Fallback: GET {origin}/.well-known/oauth-authorization-server
func discoverOAuthFromMCPURL(ctx context.Context, mcpURL string) (*DiscoveredOAuth, error) {
	parsed, err := url.Parse(mcpURL)
	if err != nil {
		return nil, fmt.Errorf("invalid MCP URL: %w", err)
	}
	origin := fmt.Sprintf("%s://%s", parsed.Scheme, parsed.Host)

	// Step 1: Try RFC 9728 Protected Resource Metadata
	authServerURL := origin
	prm, err := fetchJSON[ProtectedResourceMetadata](ctx, origin+"/.well-known/oauth-protected-resource")
	if err == nil && len(prm.AuthorizationServers) > 0 {
		authServerURL = prm.AuthorizationServers[0]
	}

	// Step 2: Try RFC 8414 Authorization Server Metadata
	meta, err := fetchJSON[OAuthServerMetadata](ctx, authServerURL+"/.well-known/oauth-authorization-server")
	if err != nil {
		// Step 3: Fallback to origin if auth server URL was different
		if authServerURL != origin {
			meta, err = fetchJSON[OAuthServerMetadata](ctx, origin+"/.well-known/oauth-authorization-server")
		}
		if err != nil {
			return nil, fmt.Errorf("no OAuth metadata found at %s: %w", origin, err)
		}
	}

	if meta.AuthorizationEndpoint == "" || meta.TokenEndpoint == "" {
		return nil, fmt.Errorf("OAuth metadata missing required endpoints")
	}

	result := &DiscoveredOAuth{
		AuthURL:              meta.AuthorizationEndpoint,
		TokenURL:             meta.TokenEndpoint,
		RegistrationEndpoint: meta.RegistrationEndpoint,
		SupportsDCR:          meta.RegistrationEndpoint != "",
	}

	// Check PKCE support
	for _, m := range meta.CodeChallengeMethodsSupported {
		if m == "S256" {
			result.SupportsPKCE = true
			break
		}
	}

	// Collect scopes
	scopes := meta.ScopesSupported
	if len(scopes) == 0 && prm != nil {
		scopes = prm.ScopesSupported
	}
	if len(scopes) > 0 {
		result.Scopes = strings.Join(scopes, " ")
	}

	return result, nil
}

// DynamicClientRegistration represents an RFC 7591 client registration request/response.
type DCRRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types,omitempty"`
	ResponseTypes           []string `json:"response_types,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

// DCRResponse holds the client credentials returned by an RFC 7591 Dynamic Client Registration endpoint.
type DCRResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
}

// dynamicClientRegister performs RFC 7591 Dynamic Client Registration.
func dynamicClientRegister(ctx context.Context, registrationURL, redirectURI string) (*DCRResponse, error) {
	reqBody := DCRRequest{
		RedirectURIs:            []string{redirectURI},
		ClientName:              "Crewship",
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		TokenEndpointAuthMethod: "none",
	}

	bodyJSON, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationURL, strings.NewReader(string(bodyJSON)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := discoveryClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DCR request failed: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("DCR returned %d: %s", resp.StatusCode, string(body))
	}

	var result DCRResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse DCR response: %w", err)
	}
	if result.ClientID == "" {
		return nil, fmt.Errorf("DCR returned empty client_id")
	}

	return &result, nil
}

// fetchJSON is a generic helper to GET a URL and decode JSON.
func fetchJSON[T any](ctx context.Context, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := discoveryClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
