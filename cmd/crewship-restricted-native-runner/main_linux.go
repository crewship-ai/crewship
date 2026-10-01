//go:build linux

// The native worker has a fixed command surface and no provider credentials.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

var nativeModel = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,95}$`)

func main() {
	if run() != nil {
		fmt.Fprintln(os.Stderr, "restricted native worker failed")
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("mode")
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "verify-project-inputs":
		if os.Getuid() != 1001 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		return verifyProjectInputs(os.Stdin)
	case "project-input-hold":
		if os.Getuid() != 1001 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		stopped, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
		defer stop()
		<-stopped.Done()
		return nil
	case "hold":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		return restrictedruntime.RunLeaseGuard(ctx)
	case "lease":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		var expiry time.Time
		if e := json.NewDecoder(io.LimitReader(os.Stdin, 4096)).Decode(&expiry); e != nil {
			return e
		}
		return restrictedruntime.RenewLease(expiry)
	case "broker":
		if os.Getuid() != 1002 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		return restrictedruntime.RunHTTPBroker(ctx, os.Stdin, os.Stdout)
	case "launch":
		if os.Getuid() != 1001 || len(os.Args) != 2 {
			return errors.New("identity")
		}
		var b restrictedruntime.Bootstrap
		d := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20))
		d.DisallowUnknownFields()
		if e := d.Decode(&b); e != nil {
			return e
		}
		if len(b.Files) != 0 || len(b.Command) != 3 || b.Command[0] != "/opt/crewship-native-runner" || b.Command[1] != "native" || !nativeModel.MatchString(b.Command[2]) || len(b.Env) != 2 || b.Env["CREWSHIP_BROKER_URL"] != "http://127.0.0.1:9121" || len(b.Env["CREWSHIP_BROKER_TOKEN"]) != 64 {
			return errors.New("launch policy")
		}
		env := []string{"HOME=/home/agent", "PATH=/usr/bin:/bin", "LANG=C.UTF-8", "CREWSHIP_BROKER_URL=" + b.Env["CREWSHIP_BROKER_URL"], "CREWSHIP_BROKER_TOKEN=" + b.Env["CREWSHIP_BROKER_TOKEN"]}
		return syscall.Exec(b.Command[0], b.Command, env)
	case "native":
		if os.Getuid() != 1001 || len(os.Args) != 3 || !nativeModel.MatchString(os.Args[2]) {
			return errors.New("identity")
		}
		return native(ctx, os.Args[2], os.Stdout)
	}
	return errors.New("mode")
}

func nativeArgs(model string) []string {
	args := []string{"exec", "--json", "--skip-git-repo-check", "--ephemeral", "--sandbox", "workspace-write", "--model", model, "-C", "/home/agent/work"}
	config := map[string]any{"model_provider": "crewship_native", "model_providers.crewship_native.name": "Crewship restricted native", "model_providers.crewship_native.base_url": "http://127.0.0.1:9121/v1", "model_providers.crewship_native.env_key": "CREWSHIP_BROKER_TOKEN", "model_providers.crewship_native.wire_api": "responses", "model_providers.crewship_native.requires_openai_auth": false, "model_providers.crewship_native.supports_websockets": false, "model_providers.crewship_native.request_max_retries": 0, "model_providers.crewship_native.stream_max_retries": 0, "approval_policy": "never", "analytics.enabled": false, "features.shell_tool": true, "web_search": "disabled", "features.goals": false, "features.hooks": false, "features.memories": false, "features.code_mode.enabled": false, "features.skill_mcp_dependency_install": false, "features.multi_agent": false, "features.apps": false, "project_root_markers": []string{}}
	// Stable order makes the pinned bootstrap's actual command inspectable.
	keys := []string{"model_provider", "model_providers.crewship_native.name", "model_providers.crewship_native.base_url", "model_providers.crewship_native.env_key", "model_providers.crewship_native.wire_api", "model_providers.crewship_native.requires_openai_auth", "model_providers.crewship_native.supports_websockets", "model_providers.crewship_native.request_max_retries", "model_providers.crewship_native.stream_max_retries", "approval_policy", "analytics.enabled", "features.shell_tool", "web_search", "features.goals", "features.hooks", "features.memories", "features.code_mode.enabled", "features.skill_mcp_dependency_install", "features.multi_agent", "features.apps", "project_root_markers"}
	for _, key := range keys {
		v, _ := json.Marshal(config[key])
		args = append(args, "-c", key+"="+string(v))
	}
	return append(args, "Complete the scoped task provided by the host.")
}

func native(ctx context.Context, model string, output io.Writer) error {
	for path, want := range map[string]string{
		"/opt/codex":     "d2752c52353401f7f6efbfcea68796f4f7a3d3e4769f5d1da53fa49d4856b72f",
		"/usr/bin/bwrap": "85580dd52ed366ece8844e90fa75ac7c4de8802963071344e123221fb9f6f11e",
	} {
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, err = io.Copy(hash, file)
		file.Close()
		if err != nil || hex.EncodeToString(hash.Sum(nil)) != want {
			return errors.New("native binary revision")
		}
	}

	if os.Getenv("CREWSHIP_BROKER_URL") != "http://127.0.0.1:9121" || len(os.Getenv("CREWSHIP_BROKER_TOKEN")) != 64 {
		return errors.New("broker policy")
	}
	for _, dir := range []string{"/home/agent/work", "/home/agent/.codex"} {
		if e := os.Mkdir(dir, 0700); e != nil {
			return e
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/opt/codex", nativeArgs(model)...)
	cmd.Dir = "/home/agent/work"
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/home/agent", "CODEX_HOME=/home/agent/.codex", "LANG=C.UTF-8", "CREWSHIP_BROKER_TOKEN=" + os.Getenv("CREWSHIP_BROKER_TOKEN")}
	reader, e := cmd.StdoutPipe()
	if e != nil {
		return e
	}
	cmd.Stderr = io.Discard
	if e = cmd.Start(); e != nil {
		return e
	}
	complete := false
	failed := false
	var textBytes int
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 256<<10)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		if complete {
			failed = true
			continue
		}
		var event struct {
			Type string `json:"type"`
			Item struct {
				Type, Status, Text string
				ExitCode           *int `json:"exit_code"`
			} `json:"item"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			failed = true
			continue
		}
		switch event.Type {
		case "turn.failed", "error":
			failed = true
		case "item.completed":
			if event.Item.Type == "command_execution" && (event.Item.Status != "completed" || event.Item.ExitCode == nil || *event.Item.ExitCode != 0) {
				failed = true
			}
			if event.Item.Type == "agent_message" {
				textBytes += len(event.Item.Text)
				if textBytes > 32768 {
					failed = true
					continue
				}
				if e = encoder.Encode(map[string]string{"type": "text", "text": event.Item.Text}); e != nil {
					cancel()
					_ = cmd.Wait()
					return e
				}
			}
		case "turn.completed":
			complete = true
		case "thread.started", "turn.started", "item.started", "item.updated":
		default:
			failed = true
		}
	}
	if scanner.Err() != nil {
		cancel()
		_ = cmd.Wait()
		return scanner.Err()
	}
	if e = cmd.Wait(); e != nil {
		return e
	}
	if !complete || failed || textBytes == 0 {
		return errors.New("native incomplete")
	}
	if e = emitArtifacts(output, "/home/agent/work"); e != nil {
		return e
	}
	return encoder.Encode(map[string]string{"type": "done"})
}
