package orchestrator

import (
	"bytes"
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

// credFileSpec is the per-file plan emitted by buildCredFileScript.
// One Credential may expand into multiple specs (USERPASS → 2 entries),
// or into one spec with a non-default mode (SSH_KEY → 0600 in ssh/
// subdir). Pulled out so tests can assert the expansion shape without
// going through a container exec.
type credFileSpec struct {
	EnvVar string // .env mapping key (e.g. GMAIL_USERNAME, GITHUB_SSH_PATH)
	Value  string // raw cleartext bytes to write to the file
	Path   string // absolute path inside the container, e.g. /secrets/agent/ssh/github
	Mode   string // octal string for chmod, e.g. "0400" or "0600"
}

// buildCredFileScript translates a slice of decrypted credentials into
// the shell script that mounts them into the agent container and the
// list of .env entries the agent reads at startup.
//
// Per-type behaviour:
//
//	API_KEY, AI_CLI_TOKEN, OAUTH2  → skipped (sidecar proxy handles them)
//	CLI_TOKEN, SECRET, GENERIC_SECRET
//	                               → one file at secretsAgentDir/<envvar>,
//	                                 mode 0400. .env maps envvar to path.
//	USERPASS                       → two files <envvar>_USERNAME and
//	                                 <envvar>_PASSWORD, mode 0400.
//	                                 Cleartext username is stored on the
//	                                 Credential struct, not encrypted (it's
//	                                 an identifier, not a secret —
//	                                 matches Bitwarden's login.username).
//	SSH_KEY                        → file at secretsAgentDir/ssh/<envvar>,
//	                                 mode 0600 (OpenSSH refuses world-
//	                                 readable keys; 0600 is the strictest
//	                                 mode the client still accepts).
//	                                 .env exposes <envvar>_PATH so the
//	                                 agent can locate the key without
//	                                 hardcoding the convention.
//	CERTIFICATE                    → file at secretsAgentDir/certs/<envvar>.pem,
//	                                 mode 0400. Same _PATH helper env var.
//
// Returns the joined-with-&& script ready for `sh -c`, the count of
// files mounted (for logging), or an error if any env var name fails
// the sanitiser. Empty input yields ("", 0, nil) so callers can early-
// exit without a noop exec.
func buildCredFileScript(creds []Credential, secretsAgentDir string) (string, int, error) {
	var specs []credFileSpec
	var envLines []string

	for _, c := range creds {
		if c.EnvVarName == "" || c.PlainValue == "" {
			continue
		}
		if !envVarNameRE.MatchString(c.EnvVarName) {
			return "", 0, fmt.Errorf("invalid credential env var name: %q", c.EnvVarName)
		}

		switch c.Type {
		case "CLI_TOKEN", "SECRET", "GENERIC_SECRET":
			path := secretsAgentDir + "/" + c.EnvVarName
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0400",
			})
			envLines = append(envLines, c.EnvVarName+"="+path)

		case "USERPASS":
			// Username is cleartext on the Credential struct; password
			// rides on PlainValue (encrypted at rest, decrypted by the
			// resolver). Both must be present — the validator at the
			// API tier enforces that, so empty username here means a
			// data-shape regression we'd rather surface than silently
			// inject "" as the username.
			if c.Username == "" {
				return "", 0, fmt.Errorf("USERPASS credential %q missing username", c.EnvVarName)
			}
			userPath := secretsAgentDir + "/" + c.EnvVarName + "_USERNAME"
			passPath := secretsAgentDir + "/" + c.EnvVarName + "_PASSWORD"
			specs = append(specs,
				credFileSpec{EnvVar: c.EnvVarName + "_USERNAME", Value: c.Username, Path: userPath, Mode: "0400"},
				credFileSpec{EnvVar: c.EnvVarName + "_PASSWORD", Value: c.PlainValue, Path: passPath, Mode: "0400"},
			)
			envLines = append(envLines,
				c.EnvVarName+"_USERNAME="+userPath,
				c.EnvVarName+"_PASSWORD="+passPath,
			)

		case "SSH_KEY":
			// 0600 (not 0400) because some SSH client builds tolerate
			// 0400 but the canonical "strict" mode for id_rsa et al.
			// is 0600 — keeping it consistent with what ssh-keygen
			// writes by default avoids "WARNING: UNPROTECTED PRIVATE
			// KEY FILE" surprises when the agent runs ssh interactively.
			path := secretsAgentDir + "/ssh/" + c.EnvVarName
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0600",
			})
			envLines = append(envLines, c.EnvVarName+"_PATH="+path)

		case "CERTIFICATE":
			// Certs aren't keys — 0400 read-only is fine and stricter
			// than 0600 (no write bit). Helper env var name mirrors
			// SSH_KEY for consistency.
			path := secretsAgentDir + "/certs/" + c.EnvVarName + ".pem"
			specs = append(specs, credFileSpec{
				EnvVar: c.EnvVarName, Value: c.PlainValue, Path: path, Mode: "0400",
			})
			envLines = append(envLines, c.EnvVarName+"_PATH="+path)

		default:
			// API_KEY, AI_CLI_TOKEN, OAUTH2, and any unknown type are
			// intentionally skipped: the sidecar proxy injects them at
			// outbound-request time so they never touch disk.
			continue
		}
	}

	if len(specs) == 0 {
		return "", 0, nil
	}

	// Pre-create the ssh/ and certs/ subdirectories with restrictive
	// perms before any file write. The script is exec'd as UID 1001
	// (matching the secretsAgentDir owner that orchestrator_run.go
	// mkdir'd before us), so file ownership lands on 1001:1001
	// automatically and we don't need chown. Earlier the script ran
	// as root and chown'd everything — that path fails silently in
	// production containers, which run with CapDrop:ALL and so
	// lack CAP_CHOWN + CAP_DAC_OVERRIDE; root inside such a container
	// can't write to a 1001-owned dir nor change ownership at all.
	//
	// TOCTOU defence on warm container restart: an agent process from
	// the previous session and the credential writer here both run as
	// UID 1001, which means the agent can plant a symlink inside
	// /secrets/<slug>/ pointing at any other 1001-writable path
	// (/crew/shared/.memory/..., /output/<other-agent>/...) and then
	// `echo … > path` follows that symlink, corrupting the linked
	// target with credential cleartext or, more usefully to the
	// attacker, with an empty .env that disables the next agent's
	// credential map. Each write therefore opens with `rm -f path`
	// first — the unlink removes the planted symlink (UID 1001 owns
	// the parent dir, so unlink succeeds regardless of the symlink's
	// target), and the subsequent shell redirect creates a fresh
	// regular file at the intended path. The two-step pattern is
	// safer than relying on `set -o noclobber` because that flag
	// makes the script fail on legitimate re-runs (re-apply of a
	// rotated credential), while rm-then-write is idempotent.
	scriptParts := []string{
		fmt.Sprintf("mkdir -p %s/ssh %s/certs", secretsAgentDir, secretsAgentDir),
		fmt.Sprintf("chmod 0700 %s/ssh %s/certs", secretsAgentDir, secretsAgentDir),
	}

	for _, s := range specs {
		// base64 round-trip prevents any shell interpretation of the
		// secret value — newlines in PEM bodies, single-quotes in
		// passwords, etc. all pass through opaquely. The leading
		// `rm -f` neutralises any pre-planted symlink (see TOCTOU
		// note on the script-parts block above) before the redirect
		// follows-or-creates.
		valB64 := base64.StdEncoding.EncodeToString([]byte(s.Value))
		scriptParts = append(scriptParts,
			fmt.Sprintf("rm -f %s", s.Path),
			fmt.Sprintf("echo '%s' | base64 -d > %s", valB64, s.Path),
			fmt.Sprintf("chmod %s %s", s.Mode, s.Path),
		)
	}

	// .env maps each env var to its file path (never the raw value), so
	// nothing sensitive ends up in /proc/<pid>/environ if the agent
	// spawns subprocesses that inherit the env block. Same `rm -f`
	// guard as the per-spec writes above.
	envContent := strings.Join(envLines, "\n") + "\n"
	envB64 := base64.StdEncoding.EncodeToString([]byte(envContent))
	envPath := secretsAgentDir + "/.env"
	scriptParts = append(scriptParts,
		fmt.Sprintf("rm -f %s", envPath),
		fmt.Sprintf("echo '%s' | base64 -d > %s", envB64, envPath),
		fmt.Sprintf("chmod 0400 %s", envPath),
		// Lock down the parent dir to 0700 so a future per-agent UID
		// layout can't list its sibling's contents. On the current
		// shared-UID setup (all agents run as 1001) this is a noop
		// but mirrors the principle-of-least-privilege intent the
		// pre-fix chown was trying to encode.
		fmt.Sprintf("chmod 0700 %s", secretsAgentDir),
	)

	return strings.Join(scriptParts, " && "), len(specs), nil
}

// writeCredentialFiles writes file-mountable credentials into the
// agent's secrets directory. Thin wrapper around buildCredFileScript
// that runs the resulting script as UID 1001 — the same UID that
// owns secretsAgentDir from the orchestrator_run.go mkdir pass.
//
// Earlier the script ran as UID 0 with chown lines, on the assumption
// that "root can do anything". That assumption is false inside Crewship's
// runtime containers: they're launched with CapDrop:["ALL"] plus
// ReadonlyRootfs and no-new-privileges, so root-without-capabilities
// can neither write to a 1001-owned dir (no CAP_DAC_OVERRIDE) nor
// chown any file (no CAP_CHOWN). The exec succeeded at the docker API
// level (returned no Go error), `io.Copy` drained an empty stdout, and
// we'd log "credential files written" while /secrets/<agent>/ stayed
// empty. Symptom in the wild: SPEC-4 sugar credentials showed up in
// agent_credentials but never reached the agent runtime, so any
// downstream code reading /secrets/<agent>/.env (or the matching
// per-credential file) saw nothing.
//
// Two changes close the gap:
//
//  1. Run as UID 1001 so the writes land via the owner-permission path
//     (no capability gymnastics needed). buildCredFileScript no longer
//     emits chown lines, which were the only ops requiring root.
//  2. After Exec returns, call ExecInspect and surface non-zero exit
//     codes as errors. Previously the orchestrator silently treated
//     "exec attached" as "exec succeeded" — the new check makes a
//     real failure (permission, disk full, sh parse error) bubble
//     up to the caller's warn-and-continue path instead of writing
//     a false-success log entry.
//
// Per-type behaviour is documented on buildCredFileScript. The
// secretsSharedDir parameter is unused today but retained on the
// signature for the crew-shared credentials work tracked separately.
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
	script, fileCount, err := buildCredFileScript(creds, secretsAgentDir)
	if err != nil {
		return fmt.Errorf("build credential script: %w", err)
	}
	if script == "" {
		return nil
	}

	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"sh", "-c", script},
		User:        "1001:1001",
	}

	result, err := ctr.Exec(ctx, cfg)
	if err != nil {
		return fmt.Errorf("write credential files: %w", err)
	}
	io.Copy(io.Discard, result.Reader)
	result.Reader.Close()

	// Reading the stream to EOF means docker has closed the exec
	// pipe, which in turn means the process has exited and
	// ExecInspect will report the final exit code without racing.
	running, exitCode, inspectErr := ctr.ExecInspect(ctx, result.ExecID)
	if inspectErr != nil {
		return fmt.Errorf("inspect credential-file exec: %w", inspectErr)
	}
	if running {
		return fmt.Errorf("credential-file exec %s reported still running after EOF", result.ExecID)
	}
	if exitCode != 0 {
		return fmt.Errorf("credential-file script exited %d (agent_slug=%s, container=%s)",
			exitCode, agentSlug, containerID)
	}

	logger.Info("credential files written",
		"agent_slug", agentSlug,
		"secrets_dir", secretsAgentDir,
		"file_count", fileCount,
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

	// Prepare memory directories with shared-write perms BEFORE launching
	// the sidecar (UID 1002). The agent home `/crew/agents/{slug}` and the
	// crew share `/crew/shared` are bind-mounted at chown 1001:1001 mode
	// 0755 by the docker init container — the sidecar can't MkdirAll into
	// either path because group/other lack write, and any pre-existing
	// `.memory` subdir inherits the same restrictive perms.
	//
	// Run a one-shot root exec that:
	//   * pre-creates the per-agent and crew-shared `.memory` directories
	//   * chowns them to user=1001 (agent) group=1002 (sidecar)
	//   * applies setgid + g+rwx so new files/dirs inherit group 1002 with
	//     the container entrypoint's umask 0002 making them g+rw
	//
	// Both UIDs can then read+write the FTS5 SQLite index and plaintext
	// markdown tier files (#530). Best-effort: failures are logged but
	// don't block sidecar startup — without these perms the path-validator
	// fallback path still works for boot-context recall.
	if memoryCfg != nil && memoryCfg.Enabled && memoryCfg.BasePath != "" {
		paths := []string{memoryCfg.BasePath}
		if memoryCfg.CrewMemoryPath != "" {
			paths = append(paths, memoryCfg.CrewMemoryPath)
		}
		// Per-path subshell `|| true` so a failure on one path (e.g.
		// the agent BasePath) doesn't block prep on the next (the crew
		// shared CrewMemoryPath). `mkdir -p -- "..."` quotes the path
		// so unusual characters can't break the script — paths today
		// come from server config but defensive quoting is cheap.
		var prepScript strings.Builder
		for _, p := range paths {
			fmt.Fprintf(&prepScript,
				`(mkdir -p -- "%s" && chown -R 1001:1002 -- "%s" && chmod -R u+rwX,g+rwXs -- "%s") || true`+"\n",
				p, p, p)
		}
		prepCfg := provider.ExecConfig{
			ContainerID: containerID,
			Cmd:         []string{"sh", "-c", prepScript.String()},
			User:        "0:0",
		}
		prepResult, prepErr := container.Exec(ctx, prepCfg)
		if prepErr != nil {
			logger.Warn("memory dir perms prep exec failed (sidecar will boot anyway)", "error", prepErr)
		} else {
			// Drain the reader first so the docker stream closes, then
			// inspect for a non-zero exit. `|| true` per path keeps the
			// script exit at 0 in normal partial-failure cases; a
			// non-zero here means a deeper docker-exec failure worth
			// surfacing (shell missing, sh -c rejected, etc.).
			var prepOut bytes.Buffer
			if prepResult != nil && prepResult.Reader != nil {
				_, _ = io.Copy(&prepOut, prepResult.Reader)
				_ = prepResult.Reader.Close()
			}
			if prepResult != nil && prepResult.ExecID != "" {
				if _, code, ierr := container.ExecInspect(ctx, prepResult.ExecID); ierr != nil {
					logger.Debug("memory dir prep inspect failed", "error", ierr)
				} else if code != 0 {
					logger.Warn("memory dir perms prep exited non-zero (sidecar will boot anyway)",
						"exit_code", code, "output", strings.TrimSpace(prepOut.String()))
				}
			}
		}
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
	switch c.EnvVarName {
	case "ANTHROPIC_API_KEY":
		return "ANTHROPIC"
	case "OPENAI_API_KEY":
		return "OPENAI"
	case "GOOGLE_API_KEY", "GEMINI_API_KEY":
		// gemini-cli accepts either GOOGLE_API_KEY or GEMINI_API_KEY; both
		// map to the same sidecar provider type.
		return "GOOGLE"
	case "CURSOR_API_KEY":
		return "CURSOR"
	case "FACTORY_API_KEY":
		return "FACTORY"
	default:
		return ""
	}
}
