package devcontainer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Typed errors for mise validation failures.
var (
	ErrMiseInvalidToolName = errors.New("mise: invalid tool name")
	ErrMiseInvalidVersion  = errors.New("mise: invalid version")
	ErrMiseInvalidEnvKey   = errors.New("mise: invalid env key")
	ErrMiseInvalidEnvValue = errors.New("mise: invalid env value")
	ErrMiseTooManyTools    = errors.New("mise: too many tools (max 20)")
	ErrMiseTooManyEnvVars  = errors.New("mise: too many env vars (max 20)")
	ErrMiseInstallFailed   = errors.New("mise: install failed")
)

var (
	toolNameRe = regexp.MustCompile(`^[a-zA-Z0-9-]{1,50}$`)
	versionRe  = regexp.MustCompile(`^[a-zA-Z0-9.\-]{1,30}$`)
	envKeyRe   = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// MiseConfig represents a mise tool configuration stored as JSON in the DB.
// It maps tool names to version strings.
type MiseConfig struct {
	Tools map[string]string `json:"tools"`        // e.g., {"node": "22", "python": "3.12"}
	Env   map[string]string `json:"env,omitempty"` // e.g., {"NODE_OPTIONS": "--max-old-space-size=4096"}
}

// ParseMiseConfig parses a JSON string into MiseConfig.
func ParseMiseConfig(data string) (*MiseConfig, error) {
	var cfg MiseConfig
	if err := json.Unmarshal([]byte(data), &cfg); err != nil {
		return nil, fmt.Errorf("mise: invalid JSON: %w", err)
	}
	if cfg.Tools == nil {
		cfg.Tools = make(map[string]string)
	}
	return &cfg, nil
}

// ToTOML converts MiseConfig to mise's native .mise.toml format.
func (c *MiseConfig) ToTOML() string {
	var b strings.Builder

	if len(c.Tools) > 0 {
		b.WriteString("[tools]\n")
		// Sort keys for deterministic output.
		keys := make([]string, 0, len(c.Tools))
		for k := range c.Tools {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(" = \"")
			b.WriteString(c.Tools[k])
			b.WriteString("\"\n")
		}
	}

	if len(c.Env) > 0 {
		if len(c.Tools) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[env]\n")
		keys := make([]string, 0, len(c.Env))
		for k := range c.Env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString(k)
			b.WriteString(" = \"")
			b.WriteString(c.Env[k])
			b.WriteString("\"\n")
		}
	}

	return b.String()
}

// Validate checks that tool names and versions are reasonable.
func (c *MiseConfig) Validate() error {
	if len(c.Tools) > 20 {
		return ErrMiseTooManyTools
	}
	if len(c.Env) > 20 {
		return ErrMiseTooManyEnvVars
	}

	for name, version := range c.Tools {
		if !toolNameRe.MatchString(name) {
			return fmt.Errorf("%w: %q", ErrMiseInvalidToolName, name)
		}
		if !versionRe.MatchString(version) {
			return fmt.Errorf("%w: %q", ErrMiseInvalidVersion, version)
		}
	}

	for key, value := range c.Env {
		if !envKeyRe.MatchString(key) {
			return fmt.Errorf("%w: %q", ErrMiseInvalidEnvKey, key)
		}
		if strings.ContainsRune(value, 0) {
			return fmt.Errorf("%w: contains null byte", ErrMiseInvalidEnvValue)
		}
	}

	return nil
}

// IsEmpty returns true if no tools are configured.
func (c *MiseConfig) IsEmpty() bool {
	return len(c.Tools) == 0
}

// ExecFunc is a function that runs a command inside a container.
// Used to decouple from Docker SDK.
type ExecFunc func(ctx context.Context, containerID string, cmd []string, user string, env []string) (stdout string, exitCode int, err error)

// InstallMise downloads and installs the mise binary inside a container.
// Runs as root (user "0:0").
func InstallMise(ctx context.Context, containerID string, exec ExecFunc) error {
	// Download and install mise.
	stdout, exitCode, err := exec(ctx, containerID, []string{
		"sh", "-c", "curl -fsSL https://mise.jdx.dev/install.sh | bash",
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("%w: download: %v", ErrMiseInstallFailed, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: download exited %d: %s", ErrMiseInstallFailed, exitCode, stdout)
	}

	// Symlink so all users can access the binary.
	stdout, exitCode, err = exec(ctx, containerID, []string{
		"ln", "-sf", "/root/.local/bin/mise", "/usr/local/bin/mise",
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("%w: symlink: %v", ErrMiseInstallFailed, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: symlink exited %d: %s", ErrMiseInstallFailed, exitCode, stdout)
	}

	// Verify installation.
	stdout, exitCode, err = exec(ctx, containerID, []string{
		"mise", "--version",
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("%w: verify: %v", ErrMiseInstallFailed, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("%w: verify exited %d: %s", ErrMiseInstallFailed, exitCode, stdout)
	}

	return nil
}

// InstallMiseTools writes the mise config and runs `mise install`.
// Runs as agent user (user "1001:1001") since mise installs to ~/.local/share/mise/.
func InstallMiseTools(ctx context.Context, containerID string, cfg *MiseConfig, exec ExecFunc) error {
	if cfg.IsEmpty() {
		return nil
	}

	toml := cfg.ToTOML()

	// Create config directory and write .mise.toml as root (then chown).
	stdout, exitCode, err := exec(ctx, containerID, []string{
		"sh", "-c", "mkdir -p /home/agent/.config/mise && cat > /home/agent/.config/mise/config.toml << 'MISE_EOF'\n" + toml + "MISE_EOF",
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("mise: write config: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise: write config exited %d: %s", exitCode, stdout)
	}

	// Set ownership to agent user.
	stdout, exitCode, err = exec(ctx, containerID, []string{
		"chown", "-R", "1001:1001", "/home/agent/.config/mise",
	}, "0:0", nil)
	if err != nil {
		return fmt.Errorf("mise: chown: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise: chown exited %d: %s", exitCode, stdout)
	}

	// Install tools as agent user.
	stdout, exitCode, err = exec(ctx, containerID, []string{
		"mise", "install", "--yes",
	}, "1001:1001", nil)
	if err != nil {
		return fmt.Errorf("mise: install tools: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise: install tools exited %d: %s", exitCode, stdout)
	}

	// Reshim to ensure shims are up to date.
	stdout, exitCode, err = exec(ctx, containerID, []string{
		"mise", "reshim",
	}, "1001:1001", nil)
	if err != nil {
		return fmt.Errorf("mise: reshim: %v", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("mise: reshim exited %d: %s", exitCode, stdout)
	}

	return nil
}
