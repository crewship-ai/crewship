package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var aiConnectionDir = func() (string, error) {
	path, err := cli.DefaultConfigPath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

type aiEntry struct {
	Raw     json.RawMessage   `json:"-"`
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
}
type aiConnectionReceipt struct {
	Client      string `json:"client"`
	Context     string `json:"context"`
	Fingerprint string `json:"fingerprint"`
	Command     string `json:"command"`
}

// Prefer the invoked symlink (e.g. Homebrew's bin/crewship), not the resolved
// Cellar path. A PATH candidate is accepted only if it is this exact executable.
func stableAIExecutable() (string, error) {
	current, err := os.Executable()
	if err != nil {
		return "", err
	}
	return chooseAIExecutable(os.Args[0], current, exec.LookPath)
}
func chooseAIExecutable(invoked, current string, lookPath func(string) (string, error)) (string, error) {
	actual, err := os.Stat(current)
	if err != nil {
		return "", err
	}
	candidates := []string{invoked, "crewship"}
	for _, candidate := range candidates {
		if !strings.ContainsAny(candidate, "/\\") {
			candidate, err = lookPath(candidate)
			if err != nil {
				continue
			}
		}
		candidate, err = filepath.Abs(candidate)
		if err != nil {
			continue
		}
		info, e := os.Stat(candidate)
		if e == nil && info.Mode().IsRegular() && os.SameFile(actual, info) {
			return candidate, nil
		}
	}
	return current, nil
}

func aiClientContext(client string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	var directory, filename string
	switch client {
	case "codex":
		directory = os.Getenv("CODEX_HOME")
		if directory == "" {
			directory = filepath.Join(home, ".codex")
		}
		filename = "config.toml"
	case "claude":
		directory = os.Getenv("CLAUDE_CONFIG_DIR")
		if directory == "" {
			return filepath.Join(home, ".claude.json"), nil
		}
		filename = ".claude.json"
	default:
		return "", apiValidation("client must be codex or claude")
	}
	return filepath.Abs(filepath.Join(directory, filename))
}
func aiDefaultSkillDirectory(client string) (string, error) {
	contextPath, err := aiClientContext(client)
	if err != nil {
		return "", err
	}
	directory := filepath.Dir(contextPath)
	if client == "claude" && os.Getenv("CLAUDE_CONFIG_DIR") == "" {
		directory = filepath.Join(directory, ".claude")
	}
	return filepath.Join(directory, "skills", "crewship"), nil
}
func aiReceiptPath(client string) (string, error) {
	contextPath, err := aiClientContext(client)
	if err != nil {
		return "", err
	}
	dir, err := aiConnectionDir()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(contextPath))
	return filepath.Join(dir, "ai-connections", client+"-"+hex.EncodeToString(hash[:8])+".json"), nil
}
func aiEntryFingerprint(entry aiEntry) string {
	if entry.Type == "" {
		entry.Type = "stdio"
	}
	raw, _ := json.Marshal(entry)
	if len(entry.Raw) > 0 {
		var all any
		dec := json.NewDecoder(bytes.NewReader(entry.Raw))
		dec.UseNumber()
		if dec.Decode(&all) == nil {
			raw, _ = json.Marshal(all)
		}
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

// Bound child output even if a broken client prints its entire configuration.
// Never forward inspection output or stderr, which can contain credentials.
type aiBoundedBuffer struct{ bytes.Buffer }

func (b *aiBoundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, fmt.Errorf("client inspection exceeds 1 MiB")
	}
	return b.Buffer.Write(p)
}

func inspectAIEntry(ctx context.Context, client string) (aiEntry, bool, error) {
	contextPath, err := aiClientContext(client)
	if err != nil {
		return aiEntry{}, false, err
	}
	if client == "claude" {
		f, err := os.Open(contextPath)
		if os.IsNotExist(err) {
			return aiEntry{}, false, nil
		}
		if err != nil {
			return aiEntry{}, false, fmt.Errorf("cannot read Claude user configuration")
		}
		defer f.Close()
		raw, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
		if err != nil || len(raw) > 4<<20 {
			return aiEntry{}, false, fmt.Errorf("Claude configuration unreadable or too large")
		}
		var cfg struct {
			Servers map[string]json.RawMessage `json:"mcpServers"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return aiEntry{}, false, fmt.Errorf("invalid Claude user configuration")
		}
		rawEntry, ok := cfg.Servers["crewship"]
		var entry aiEntry
		if ok {
			if json.Unmarshal(rawEntry, &entry) != nil {
				return aiEntry{}, true, fmt.Errorf("invalid Crewship client entry")
			}
			entry.Raw = rawEntry
		}
		return entry, ok, nil
	}
	path, err := exec.LookPath("codex")
	if err != nil {
		return aiEntry{}, false, fmt.Errorf("Codex is not installed on PATH")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, path, "mcp", "get", "crewship", "--json")
	var out, diagnostics aiBoundedBuffer
	child.Stdout = &out
	child.Stderr = &diagnostics
	err = child.Run()
	if err != nil {
		if strings.Contains(diagnostics.String(), "No MCP server named 'crewship' found.") {
			return aiEntry{}, false, nil
		}
		return aiEntry{}, false, fmt.Errorf("Codex MCP inspection failed; inspect the client configuration locally")
	}
	var config struct {
		Transport aiEntry `json:"transport"`
	}
	if json.Unmarshal(out.Bytes(), &config) != nil || config.Transport.Type != "stdio" {
		return aiEntry{}, true, fmt.Errorf("Crewship entry is not a supported stdio configuration")
	}
	config.Transport.Raw = append(json.RawMessage(nil), out.Bytes()...)
	return config.Transport, true, nil
}

func readAIReceipt(client string) (aiConnectionReceipt, error) {
	path, err := aiReceiptPath(client)
	if err != nil {
		return aiConnectionReceipt{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return aiConnectionReceipt{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return aiConnectionReceipt{}, fmt.Errorf("invalid connection receipt")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return aiConnectionReceipt{}, err
	}
	var receipt aiConnectionReceipt
	if err = json.Unmarshal(raw, &receipt); err != nil {
		return receipt, fmt.Errorf("invalid connection receipt")
	}
	contextPath, err := aiClientContext(client)
	if err != nil {
		return receipt, err
	}
	if receipt.Client != client || receipt.Context != contextPath {
		return receipt, fmt.Errorf("connection receipt belongs to a different client context")
	}
	return receipt, nil
}
func ownsAIEntry(client string, entry aiEntry) bool {
	receipt, err := readAIReceipt(client)
	return err == nil && receipt.Fingerprint == aiEntryFingerprint(entry)
}
func saveAIReceipt(client string, entry aiEntry) error {
	path, err := aiReceiptPath(client)
	if err != nil {
		return err
	}
	contextPath, err := aiClientContext(client)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if info, e := os.Lstat(path); e == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular connection receipt")
	} else if e != nil && !os.IsNotExist(e) {
		return e
	}
	receipt := aiConnectionReceipt{Client: client, Context: contextPath, Fingerprint: aiEntryFingerprint(entry), Command: entry.Command}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func connectAIClient(cmd *cobra.Command, client, executable string, args, registration []string) error {
	existing, found, err := inspectAIEntry(cmd.Context(), client)
	if err != nil {
		return err
	}
	desired := aiEntry{Type: "stdio", Command: executable, Args: args}
	if found && !ownsAIEntry(client, existing) {
		return apiValidation("existing Crewship client entry is unmanaged or modified; inspect it and remove it explicitly before connecting")
	}
	installSkill, _ := cmd.Flags().GetBool("install-skill")
	if installSkill {
		directory, err := aiDefaultSkillDirectory(client)
		if err != nil {
			return err
		}
		if err = installAISkill(directory); err != nil {
			return err
		}
	}
	if found && existing.Command == desired.Command && reflect.DeepEqual(existing.Args, desired.Args) {
		return nil
	}
	path, err := exec.LookPath(client)
	if err != nil {
		return fmt.Errorf("%s is not installed on PATH; use crewship ai config instead", client)
	}
	// The native CLI owns config syntax and updates. Only our exact prior entry
	// may be replaced; custom or subsequently edited entries are never removed.
	if found {
		if err = removeAIClient(cmd, client); err != nil {
			return err
		}
	}
	childCtx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(childCtx, path, registration...)
	child.Stdin = cmd.InOrStdin()
	child.Stdout = cmd.ErrOrStderr()
	child.Stderr = cmd.ErrOrStderr()
	if err = child.Run(); err != nil {
		return err
	}
	actual, found, err := inspectAIEntry(cmd.Context(), client)
	if err != nil || !found || actual.Type != "stdio" || actual.Command != desired.Command || !reflect.DeepEqual(actual.Args, desired.Args) || len(actual.Env) > 0 {
		return fmt.Errorf("client registration could not be verified; inspect the native client configuration")
	}
	return saveAIReceipt(client, actual)
}

func removeAIClient(cmd *cobra.Command, client string) error {
	entry, found, err := inspectAIEntry(cmd.Context(), client)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if !ownsAIEntry(client, entry) {
		return apiValidation("refusing to remove an unmanaged or modified Crewship client entry")
	}
	argv := []string{"mcp", "remove", "crewship"}
	if client == "claude" {
		argv = []string{"mcp", "remove", "--scope", "user", "crewship"}
	}
	childCtx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(childCtx, client, argv...)
	child.Stdout = cmd.ErrOrStderr()
	child.Stderr = cmd.ErrOrStderr()
	if err = child.Run(); err != nil {
		return err
	}
	_, found, err = inspectAIEntry(cmd.Context(), client)
	if err != nil {
		return err
	}
	if found {
		return fmt.Errorf("client still reports the Crewship registration")
	}
	path, err := aiReceiptPath(client)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func newAIStatusCommand(doctor bool) *cobra.Command {
	name := "status"
	description := "Inspect native client registrations and ownership without exposing credentials"
	if doctor {
		name = "doctor"
		description = "Diagnose missing clients, missing binaries and modified registrations"
	}
	return &cobra.Command{Use: name + " [codex|claude]", Short: description, Args: apiArgs(cobra.MaximumNArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		clients := []string{"codex", "claude"}
		if len(args) > 0 {
			clients = args
		}
		reports := []map[string]any{}
		for _, client := range clients {
			if _, err := aiClientContext(client); err != nil {
				return err
			}
			report := map[string]any{"client": client, "registered": false, "owned": false}
			path, lookupErr := exec.LookPath(client)
			report["client_installed"] = lookupErr == nil
			entry, found, err := inspectAIEntry(cmd.Context(), client)
			if err != nil {
				report["problem"] = err.Error()
			} else {
				report["registered"] = found
				if found {
					report["owned"] = ownsAIEntry(client, entry)
					report["command"] = entry.Command
					if doctor {
						linkInfo, linkErr := os.Lstat(entry.Command)
						report["stable_symlink"] = linkErr == nil && linkInfo.Mode()&os.ModeSymlink != 0
						if strings.Contains(filepath.ToSlash(entry.Command), "/Cellar/") || strings.HasPrefix(entry.Command, os.TempDir()+string(os.PathSeparator)) {
							report["installation_warning"] = "temporary or versioned executable path; reconnect from a permanent installation path"
						}
					}
					_, executableErr := exec.LookPath(entry.Command)
					report["executable_exists"] = executableErr == nil
					if executableErr != nil {
						report["problem"] = "registered executable is missing or not executable; reconnect from the installed stable path"
					}
					if !ownsAIEntry(client, entry) {
						report["problem"] = "entry is unmanaged or changed; automatic removal is refused"
					}
				}
			}
			if doctor && lookupErr == nil {
				report["client_path"] = path
			}
			reports = append(reports, report)
		}
		return apiStructuredOutput(cmd, reports)
	}}
}
func newAIDisconnectCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "disconnect <codex|claude>", Short: "Remove only an unchanged Crewship registration created by ai connect", Args: apiArgs(cobra.ExactArgs(1)), RunE: func(cmd *cobra.Command, args []string) error {
		if _, err := aiClientContext(args[0]); err != nil {
			return err
		}
		dry, _ := cmd.Flags().GetBool("dry-run")
		if dry {
			entry, found, err := inspectAIEntry(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if found && !ownsAIEntry(args[0], entry) {
				return errors.New("entry is unmanaged or modified; removal refused")
			}
			return apiStructuredOutput(cmd, map[string]any{"client": args[0], "would_remove": found, "dry_run": true})
		}
		return removeAIClient(cmd, args[0])
	}}
	cmd.Flags().Bool("dry-run", false, "Inspect ownership and report the removal without changing client configuration")
	return cmd
}
