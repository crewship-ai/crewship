package orchestrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/provider"
)

// PreRunInstallPackages installs system packages as root before the agent starts.
// The agent runs as UID 1001 (non-root) and cannot install apt packages itself.
// This function runs `apt-get install` as root (UID 0), then the agent exec
// runs as UID 1001 with the packages available in PATH.
func PreRunInstallPackages(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	packages []string,
	logger *slog.Logger,
) error {
	if len(packages) == 0 {
		return nil
	}

	// Sanitize package names: only allow alphanumeric, dash, dot, plus
	for _, pkg := range packages {
		for _, c := range pkg {
			if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '+') {
				return fmt.Errorf("invalid package name: %q", pkg)
			}
		}
	}

	script := "apt-get update -qq && apt-get install -y -qq " + strings.Join(packages, " ")
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "0:0",
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("pre-run install: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Info("pre-run packages installed",
		"container_id", containerID[:min(12, len(containerID))],
		"packages", packages,
	)
	return nil
}

// writeCredentialFiles writes CLI_TOKEN and SECRET credentials as individual files
// into the agent's secrets directory. Each credential is written as a separate file
// named after its env var (e.g., /secrets/{agent-slug}/GH_TOKEN). A combined .env
// file is also generated for tools that source environment files.
// Files are written as root (UID 0) then chowned to 1001:1001 with mode 0400 (read-only).
func writeCredentialFiles(
	ctx context.Context,
	ctr provider.ContainerProvider,
	containerID string,
	agentSlug string,
	creds []Credential,
	secretsAgentDir string,
	secretsSharedDir string,
	logger *slog.Logger,
) error {
	// Collect credentials that should be written as files.
	// API_KEY and AI_CLI_TOKEN are handled by the sidecar proxy — not written to disk.
	type credFile struct {
		EnvVar string
		Value  string
	}
	var files []credFile
	for _, c := range creds {
		if (c.Type == "CLI_TOKEN" || c.Type == "SECRET") && c.EnvVarName != "" && c.PlainValue != "" {
			if !envVarNameRE.MatchString(c.EnvVarName) {
				return fmt.Errorf("invalid credential env var name: %q", c.EnvVarName)
			}
			files = append(files, credFile{EnvVar: c.EnvVarName, Value: c.PlainValue})
		}
	}

	if len(files) == 0 {
		return nil
	}

	// Build a shell script that writes each credential as a file and generates .env.
	// Uses base64 encoding to prevent shell injection from credential values.
	var scriptParts []string
	var envLines []string

	for _, f := range files {
		valB64 := base64.StdEncoding.EncodeToString([]byte(f.Value))
		filePath := secretsAgentDir + "/" + f.EnvVar
		scriptParts = append(scriptParts,
			fmt.Sprintf("echo '%s' | base64 -d > %s", valB64, filePath),
			fmt.Sprintf("chown 1001:1001 %s", filePath),
			fmt.Sprintf("chmod 0400 %s", filePath),
		)
		envLines = append(envLines, f.EnvVar+"="+filePath)
	}

	// Write .env file (maps env var names to file paths, not raw values)
	envContent := strings.Join(envLines, "\n") + "\n"
	envB64 := base64.StdEncoding.EncodeToString([]byte(envContent))
	envPath := secretsAgentDir + "/.env"
	scriptParts = append(scriptParts,
		fmt.Sprintf("echo '%s' | base64 -d > %s", envB64, envPath),
		fmt.Sprintf("chown 1001:1001 %s", envPath),
		fmt.Sprintf("chmod 0400 %s", envPath),
	)

	// Chown the secrets dir itself (not recursively) and each file individually.
	// Chowning individual files rather than the parent dir prevents agents sharing
	// UID 1001 from traversing or listing sibling agents' secret directories.
	scriptParts = append(scriptParts,
		fmt.Sprintf("chown 1001:1001 %s", secretsAgentDir),
	)

	script := strings.Join(scriptParts, " && ")

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "0:0",
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write credential files: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	logger.Info("credential files written",
		"agent_slug", agentSlug,
		"secrets_dir", secretsAgentDir,
		"file_count", len(files),
	)
	return nil
}

// sidecarHealth holds the parsed health response from a running sidecar.
type sidecarHealth struct {
	Status      string `json:"status"`
	NetworkMode string `json:"network_mode"`
}

// checkSidecar checks if a sidecar proxy is already listening on port 9119
// inside the given container. Returns nil if not running. If running, returns
// its current health state including network_mode.
func checkSidecar(ctx context.Context, ctr provider.ContainerProvider, containerID string) *sidecarHealth {
	if ctr == nil {
		return nil
	}
	result, err := ctr.Exec(ctx, provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", "curl -sf http://127.0.0.1:9119/health 2>/dev/null || wget -q -O - http://127.0.0.1:9119/health 2>/dev/null"},
		User:        "1002:1002",
	})
	if err != nil {
		return nil
	}
	output, _ := io.ReadAll(result.Reader)
	result.Reader.Close()
	var h sidecarHealth
	if err := json.Unmarshal(output, &h); err != nil {
		return nil
	}
	if h.Status != "ok" {
		return nil
	}
	return &h
}

// startSidecar launches the crewship-sidecar proxy inside the container.
// It pipes credentials via stdin JSON and waits for the "SIDECAR_READY" signal.
// The sidecar runs as a background process and intercepts all agent HTTP traffic.
// SidecarMemoryConfig is passed to the sidecar binary via stdin when memory is enabled.
type SidecarMemoryConfig struct {
	Enabled        bool   `json:"enabled"`
	BasePath       string `json:"base_path"`
	AgentSlug      string `json:"agent_slug"`
	AgentRole      string `json:"agent_role"`       // "lead" or "agent"
	CrewMemoryPath string `json:"crew_memory_path"` // e.g. /crew/shared/.memory
}

// SidecarIPCConfig provides the crewshipd internal API address for the sidecar,
// allowing lead agents to forward assignment requests back to crewshipd.
// ContainerID is the Docker container ID where this agent is running; the sidecar
// forwards it to crewshipd so /keeper/execute can run commands in the right container.
type SidecarIPCConfig struct {
	BaseURL     string `json:"base_url"`
	Token       string `json:"token"`
	AgentID     string `json:"agent_id"`
	AgentSlug   string `json:"agent_slug"`
	CrewID      string `json:"crew_id"`
	WorkspaceID string `json:"workspace_id"`
	ChatID      string `json:"chat_id"`
	ContainerID string `json:"container_id"`
}

// SidecarCrewMember describes a crew member accessible to lead agents for assignment.
type SidecarCrewMember struct {
	ID        string `json:"id"`
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	RoleTitle string `json:"role_title"`
	ChatID    string `json:"chat_id,omitempty"`
}

// SidecarNetworkPolicy configures crew-level network access for the sidecar.
type SidecarNetworkPolicy struct {
	Mode           string   `json:"mode"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

func startSidecar(
	ctx context.Context,
	container provider.ContainerProvider,
	containerID string,
	creds []Credential,
	memoryCfg *SidecarMemoryConfig,
	ipcCfg *SidecarIPCConfig,
	crewMembers []SidecarCrewMember,
	networkPolicy *SidecarNetworkPolicy,
	mcpServers []MCPServerConfig,
	logger *slog.Logger,
) error {
	type sidecarCred struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Token    string `json:"token"`
		Priority int    `json:"priority"`
	}

	var sc []sidecarCred
	for _, c := range creds {
		prov := credTypeToProvider(c)
		if prov == "" {
			continue
		}
		sc = append(sc, sidecarCred{
			ID:       c.ID,
			Provider: prov,
			Token:    c.PlainValue,
			Priority: c.Priority,
		})
	}
	if len(sc) == 0 {
		sc = []sidecarCred{}
	}

	// Build the input payload (new object format that includes memory config and IPC config)
	type sidecarMCPServer struct {
		ID          string            `json:"id"`
		Name        string            `json:"name"`
		DisplayName string            `json:"display_name"`
		Transport   string            `json:"transport"`
		Endpoint    string            `json:"endpoint,omitempty"`
		Command     string            `json:"command,omitempty"`
		Args        []string          `json:"args,omitempty"`
		Env         map[string]string `json:"env,omitempty"`
		Credential  *MCPCredential    `json:"credential,omitempty"`
	}
	type sidecarInput struct {
		Credentials   []sidecarCred         `json:"credentials"`
		Memory        *SidecarMemoryConfig  `json:"memory,omitempty"`
		IPC           *SidecarIPCConfig     `json:"ipc,omitempty"`
		CrewMembers   []SidecarCrewMember   `json:"crew_members,omitempty"`
		NetworkPolicy *SidecarNetworkPolicy `json:"network_policy,omitempty"`
		MCPServers    []sidecarMCPServer    `json:"mcp_servers,omitempty"`
	}

	// Only pass HTTP servers to sidecar — stdio servers are handled
	// by Claude Code directly via .mcp.json, not the gateway.
	var mcpInput []sidecarMCPServer
	for _, s := range mcpServers {
		if s.Transport != "streamable-http" {
			continue
		}
		// sidecarMCPServer has identical fields & JSON tags to
		// MCPServerConfig; the anonymous type exists only so the JSON
		// envelope stays scoped to this function. A direct conversion
		// keeps the two in lockstep — field-by-field copy would silently
		// drift if orchestrator.MCPServerConfig gains a field.
		mcpInput = append(mcpInput, sidecarMCPServer(s))
	}

	input := sidecarInput{
		Credentials:   sc,
		Memory:        memoryCfg,
		IPC:           ipcCfg,
		CrewMembers:   crewMembers,
		NetworkPolicy: networkPolicy,
		MCPServers:    mcpInput,
	}

	credsJSON, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal sidecar input: %w", err)
	}

	// SECURITY: Base64-encode the credentials JSON to prevent shell injection.
	// Raw JSON piped through `echo '...'` is vulnerable to shell metacharacter
	// injection if a credential token contains single quotes or other shell chars.
	credsB64 := base64.StdEncoding.EncodeToString(credsJSON)

	// Start sidecar as a background process.
	// Pipe credentials JSON via base64-decoded stdin to avoid shell injection.
	// Redirect stdout/stderr to files so the sidecar survives after Docker exec
	// stream closes (writes to closed pipes cause SIGPIPE which kills the process).
	// Health check: verify sidecar is responding, exit 1 on failure so orchestrator knows.
	script := fmt.Sprintf(
		`echo '%s' | base64 -d | crewship-sidecar --addr 127.0.0.1:9119 >/dev/null 2>>/tmp/sidecar.log &`+
			"\n"+`sleep 0.5`+"\n"+
			`if wget -q -O /dev/null http://127.0.0.1:9119/health 2>/dev/null; then exit 0; `+
			`elif curl -sf http://127.0.0.1:9119/health >/dev/null 2>&1; then exit 0; `+
			`else echo "sidecar health check failed" >&2; exit 1; fi`,
		credsB64,
	)

	// SECURITY: Run sidecar as UID 1002 (not 1001) so the agent process
	// cannot read /proc/<sidecar_pid>/mem to extract credentials from heap.
	// Linux kernel restricts /proc/PID/mem access to same-UID processes.
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1002:1002",
	}

	result, err := container.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("start sidecar: %w", err)
	}

	output, readErr := io.ReadAll(result.Reader)
	result.Reader.Close()

	// Check if the health check script exited with an error
	running, exitCode, inspErr := container.ExecInspect(ctx, result.ExecID)
	if inspErr != nil {
		return fmt.Errorf("inspect sidecar exec: %w", inspErr)
	}
	if !running && exitCode != 0 {
		msg := strings.TrimSpace(string(output))
		if readErr != nil {
			msg += fmt.Sprintf(" (read error: %v)", readErr)
		}
		return fmt.Errorf("sidecar health check failed (exit %d): %s", exitCode, msg)
	}

	logger.Info("sidecar started",
		"container_id", containerID[:min(12, len(containerID))],
		"credentials", len(sc),
		"output_bytes", len(output),
	)
	return nil
}

// credTypeToProvider maps orchestrator credential types to sidecar provider types.
// AI_CLI_TOKEN (OAuth) returns "" — these are injected directly as CLAUDE_CODE_OAUTH_TOKEN
// env var in BuildEnvVarsSidecar rather than stored in the sidecar CredStore, because
// the sidecar CredStore only supports x-api-key injection which won't work for OAuth tokens.
func credTypeToProvider(c Credential) string {
	switch {
	case c.EnvVarName == "ANTHROPIC_API_KEY":
		return "ANTHROPIC"
	case c.EnvVarName == "OPENAI_API_KEY":
		return "OPENAI"
	case c.EnvVarName == "GOOGLE_API_KEY":
		return "GOOGLE"
	default:
		return ""
	}
}
