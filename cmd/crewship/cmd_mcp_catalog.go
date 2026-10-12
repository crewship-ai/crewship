package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/cli"
)

type mcpCatalogInfo struct {
	Source            string `json:"source" yaml:"source"`
	Version           string `json:"version,omitempty" yaml:"version,omitempty"`
	SHA256            string `json:"sha256,omitempty" yaml:"sha256,omitempty"`
	FallbackReason    string `json:"fallback_reason,omitempty" yaml:"fallback_reason,omitempty"`
	IgnoredOperations int    `json:"ignored_operations,omitempty" yaml:"ignored_operations,omitempty"`
}

// Capture the credential source and target once, but read the token afresh.
// No global CLI configuration is mutated by concurrent tool calls.
func newMCPCredentialSource(base *cli.Client, configPath ...string) (func() (*cli.Client, error), error) {
	profile, err := aiProfile()
	if err != nil {
		return nil, err
	}
	path, err := cli.DefaultConfigPath()
	if err != nil {
		return nil, err
	}
	if len(configPath) > 0 && configPath[0] != "" {
		path = configPath[0]
	}
	token := cli.EnvToken()
	origin := base.BaseURL
	if cliCfg != nil && cliCfg.Server != "" {
		origin = cliCfg.Server
	}
	return func() (*cli.Client, error) {
		current := *base
		if token != "" {
			current.Token = token
			return &current, nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, cli.WithExitCode(fmt.Errorf("MCP credentials unavailable; run crewship login for the pinned profile"), cli.ExitAuth)
		}
		var cfg cli.CLIConfig
		if yaml.Unmarshal(raw, &cfg) != nil {
			return nil, cli.WithExitCode(fmt.Errorf("MCP credential configuration is invalid"), cli.ExitAuth)
		}
		server, credential := cfg.Server, cfg.Token
		if profile != "" {
			selected := cfg.Servers[profile]
			if selected == nil {
				return nil, cli.WithExitCode(fmt.Errorf("MCP pinned profile no longer exists; reconnect explicitly"), cli.ExitAuth)
			}
			server, credential = selected.Server, selected.Token
		}
		if strings.TrimRight(server, "/") != strings.TrimRight(origin, "/") {
			return nil, cli.WithExitCode(fmt.Errorf("MCP credential server changed; reconnect explicitly before changing identity"), cli.ExitAuth)
		}
		if strings.TrimSpace(credential) == "" {
			return nil, cli.WithExitCode(fmt.Errorf("not logged in; run crewship login for the pinned profile"), cli.ExitAuth)
		}
		current.Token = credential
		return &current, nil
	}, nil
}

func (s *cliMCP) loadCatalog(ctx context.Context, mode string) error {
	s.catalog = mcpCatalogInfo{Source: "embedded"}
	raw, _ := json.Marshal(s.doc)
	hash := sha256.Sum256(raw)
	s.catalog.SHA256 = hex.EncodeToString(hash[:])
	if v, ok := s.doc.Info["version"].(string); ok {
		s.catalog.Version = v
	}
	if mode == "embedded" {
		return nil
	}
	if mode != "auto" && mode != "server" {
		return apiValidation("catalog must be auto, server, or embedded")
	}
	remote, info, err := s.fetchCatalog(ctx)
	var operations []apiOperation
	if err == nil {
		operations, err = remote.operations("", "")
		if err != nil {
			err = fmt.Errorf("server catalog operation metadata is invalid")
		}
	}
	if err != nil {
		// Never surface a remote response body or credential parser input.
		s.catalog.FallbackReason = err.Error()
		if mode == "server" {
			return err
		}
		return nil
	}
	s.doc, s.catalog, s.operations = remote, info, operations
	return nil
}

func (s *cliMCP) fetchCatalog(ctx context.Context) (*apiDocument, mcpCatalogInfo, error) {
	info := mcpCatalogInfo{Source: "server"}
	ctx, cancel := context.WithTimeout(ctx, min(s.timeout, 5*time.Second))
	defer cancel()
	client, err := s.refreshClient()
	if err != nil {
		return nil, info, fmt.Errorf("server catalog unavailable: authentication required")
	}
	clone := *client
	clone.WorkspaceID = "" // OpenAPI is not workspace-scoped; no slug preflight.
	hc := *client.HTTPClient
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	clone.HTTPClient = &hc
	req, err := clone.NewRequest(ctx, http.MethodGet, "/openapi.json", nil)
	if err != nil {
		return nil, info, fmt.Errorf("server catalog request refused")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, info, fmt.Errorf("server catalog unavailable: transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, info, fmt.Errorf("server catalog unavailable: HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (10<<20)+1))
	if err != nil || len(raw) > 10<<20 {
		return nil, info, fmt.Errorf("server catalog unreadable or exceeds 10 MiB")
	}
	var remote apiDocument
	if err := json.Unmarshal(raw, &remote); err != nil || !strings.HasPrefix(remote.OpenAPI, "3.") || len(remote.Paths) == 0 {
		return nil, info, fmt.Errorf("server catalog is not a supported OpenAPI document")
	}
	if err := validateMCPCatalogJSON(raw); err != nil {
		return nil, info, err
	}
	// Only routes this binary understands are callable. In particular an
	// arbitrary remote readOnly flag, operationId or tag can never grant access.
	safePaths := map[string]map[string]json.RawMessage{}
	total := 0
	for _, item := range remote.Paths {
		for method := range item {
			if apiHTTPMethod(strings.ToUpper(method)) {
				total++
			}
		}
	}
	retained := 0
	for _, local := range s.operations {
		method := strings.ToLower(local.Method)
		item := remote.Paths[local.Path]
		rawOp, ok := item[method]
		if !ok {
			continue
		}
		var op map[string]json.RawMessage
		if json.Unmarshal(rawOp, &op) != nil || op == nil {
			return nil, info, fmt.Errorf("server catalog operation is invalid")
		}
		for key, fallback := range map[string]string{"summary": local.Summary, "description": local.Description} {
			var description string
			if len(op[key]) == 0 || (json.Unmarshal(op[key], &description) == nil && strings.TrimSpace(description) == "") {
				op[key], _ = json.Marshal(fallback)
			}
		}
		op["operationId"], _ = json.Marshal(local.ID)
		op["tags"], _ = json.Marshal(local.Tags)
		op["x-crewship-read-only"], _ = json.Marshal(!local.RequiresYes)
		encoded, _ := json.Marshal(op)
		if safePaths[local.Path] == nil {
			safePaths[local.Path] = map[string]json.RawMessage{}
			if parameters, ok := item["parameters"]; ok {
				safePaths[local.Path]["parameters"] = parameters
			}
		}
		safePaths[local.Path][method] = encoded
		retained++
	}
	if retained == 0 {
		return nil, info, fmt.Errorf("server catalog has no operations supported by this binary")
	}
	remote.Paths = safePaths
	info.IgnoredOperations = total - retained
	hash := sha256.Sum256(raw)
	info.SHA256 = hex.EncodeToString(hash[:])
	info.Version, _ = remote.Info["version"].(string)
	return &remote, info, nil
}

func validateMCPCatalogJSON(raw []byte) error {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return fmt.Errorf("invalid server catalog")
	}
	var walk func(any, int) error
	walk = func(v any, depth int) error {
		if depth > 64 {
			return fmt.Errorf("server catalog nesting exceeds 64")
		}
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok && !strings.HasPrefix(ref, "#/components/") {
				return fmt.Errorf("server catalog contains unsupported schema references")
			}
			for _, child := range x {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range x {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value, 0)
}
