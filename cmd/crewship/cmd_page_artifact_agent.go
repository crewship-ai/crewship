package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func artifactCLIRequest(cmd *cobra.Command, method, path string, body any) error {
	return artifactCLIRequestWithTransport(cmd, method, path, body, nil)
}

// A protected run never falls back to a saved human profile or an environment
// server URL. Human invocations retain the established authenticated Page CLI.
func artifactCLIRequestWithTransport(cmd *cobra.Command, method, path string, body any, transport http.RoundTripper) error {
	token := os.Getenv("CREWSHIP_AGENT_TOKEN")
	if token == "" {
		return projectCLIRequest(cmd, method, path, body)
	}
	if !strings.HasPrefix(token, "agtv2.") || len(token) > 2048 || strings.ContainsAny(token, " \r\n\t") {
		return errors.New("original live run capability required; no human profile fallback")
	}
	for _, name := range []string{"config", "server", "profile", "workspace", "server-allow-mismatch"} {
		if cmd.Flags().Changed(name) || cmd.InheritedFlags().Changed(name) {
			return errors.New("artifact run commands do not accept config/server/profile/workspace overrides")
		}
	}
	parsed, err := url.Parse(path)
	if err != nil {
		return err
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	arguments := map[string]any{}
	if body != nil {
		raw, e := json.Marshal(body)
		if e != nil {
			return e
		}
		if json.Unmarshal(raw, &arguments) != nil {
			return errors.New("artifact request object required")
		}
	}
	operation := ""
	if parsed.Path == "/api/v1/pages/artifacts" {
		operation = "artifact_create"
	} else if len(parts) >= 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "pages" && parts[4] == "artifact" {
		arguments["slug"] = parts[3]
		suffix := strings.Join(parts[5:], "/")
		switch {
		case method == "GET" && suffix == "":
			operation = "artifact_versions"
		case method == "GET" && suffix == "comments":
			operation = "artifact_comments"
		case method == "POST" && suffix == "versions":
			operation = "artifact_publish"
		case method == "POST" && suffix == "restore":
			operation = "artifact_restore"
		case method == "POST" && len(parts) == 8 && parts[5] == "comments" && parts[7] == "reply":
			operation = "artifact_reply"
			arguments["comment_id"] = parts[6]
		}
	}
	if operation == "" {
		return errors.New("artifact operation unavailable to an agent run")
	}
	if version := parsed.Query().Get("version"); version != "" {
		number, e := strconv.ParseInt(version, 10, 64)
		if e != nil {
			return e
		}
		arguments["version"] = number
	}
	packet, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": operation, "arguments": arguments}})
	if err != nil || len(packet) > 1<<20 {
		return errors.New("artifact tool request exceeds 1 MiB")
	}
	req, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, selectedAgentOrigin+"/mcp/routines", bytes.NewReader(packet))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if transport == nil {
		owned := &http.Transport{Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 10 * time.Second}
		defer owned.CloseIdleConnections()
		transport = owned
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("protected artifact redirects are forbidden")
	}}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("artifact outcome unavailable; preserve candidate and inspect before retrying")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, (3<<20)+1))
	if err != nil || len(raw) > 3<<20 {
		return errors.New("artifact tool response exceeds limit")
	}
	raw = bytes.ReplaceAll(raw, []byte(token), []byte("[REDACTED]"))
	var envelope struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if response.StatusCode != 200 || json.Unmarshal(raw, &envelope) != nil || len(envelope.Error) != 0 || len(envelope.Result.Content) != 1 {
		return errors.New("artifact tool outcome unavailable; inspect project before retrying")
	}
	result := []byte(envelope.Result.Content[0].Text)
	if envelope.Result.IsError {
		if len(result) > 1024 {
			result = []byte(`{"error":"artifact operation refused"}`)
		}
		return fmt.Errorf("artifact tool refused: %s", result)
	}
	if !json.Valid(result) {
		return errors.New("artifact tool returned invalid JSON")
	}
	return pageEmitMachine(resolvedFormatter(cmd), result, "{}")
}
