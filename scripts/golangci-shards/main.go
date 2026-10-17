// Command golangci-shards partitions the canonical lint configuration into
// checks, then unused. Both mandatory invocations retain their five-minute
// deadline; the existing CI step supplies their shared eight-minute deadline.
// Run from the repository root: go run ./scripts/golangci-shards /path/to/golangci-lint
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type partition struct {
	Name   string
	Config []byte
}

func mapping(value map[string]any, name string) (map[string]any, error) {
	result, ok := value[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("configuration requires mapping %s", name)
	}
	return result, nil
}

func exactList(value any, expected ...string) bool {
	items, ok := value.([]any)
	if !ok || len(items) != len(expected) {
		return false
	}
	names := make([]string, len(items))
	for i, item := range items {
		var valid bool
		names[i], valid = item.(string)
		if !valid {
			return false
		}
	}
	slices.Sort(names)
	slices.Sort(expected)
	return slices.Equal(names, expected)
}

func partitions(raw []byte) ([]partition, error) {
	var original map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&original); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("one canonical YAML document required")
	}
	linters, err := mapping(original, "linters")
	if err != nil {
		return nil, err
	}
	formatters, err := mapping(original, "formatters")
	if err != nil {
		return nil, err
	}
	runConfig, err := mapping(original, "run")
	if err != nil {
		return nil, err
	}
	issues, err := mapping(original, "issues")
	if err != nil {
		return nil, err
	}
	settings, err := mapping(linters, "settings")
	if err != nil {
		return nil, err
	}
	staticcheck, err := mapping(settings, "staticcheck")
	if err != nil {
		return nil, err
	}
	if original["version"] != "2" || linters["default"] != "none" ||
		!exactList(linters["enable"], "govet", "ineffassign", "staticcheck", "unused") ||
		!exactList(formatters["enable"], "gofmt") ||
		runConfig["tests"] != true || runConfig["timeout"] != "5m" ||
		issues["max-issues-per-linter"] != 0 || issues["max-same-issues"] != 0 ||
		!exactList(staticcheck["checks"], "SA*", "-ST1005") {
		return nil, errors.New("canonical lint coverage changed; review the partition contract")
	}
	if disabled, exists := linters["disable"]; exists && !exactList(disabled) {
		return nil, errors.New("disabled linters require partition review")
	}
	var result []partition
	seen := map[string]bool{}
	for _, group := range []struct {
		name                string
		linters, formatters []any
	}{
		{"checks", []any{"govet", "ineffassign", "staticcheck"}, []any{"gofmt"}},
		{"unused", []any{"unused"}, []any{}},
	} {
		var copy map[string]any
		if err := yaml.Unmarshal(raw, &copy); err != nil {
			return nil, err
		}
		copyLinters, _ := mapping(copy, "linters")
		copyFormatters, _ := mapping(copy, "formatters")
		copyLinters["enable"], copyFormatters["enable"] = group.linters, group.formatters
		for _, name := range append(slices.Clone(group.linters), group.formatters...) {
			key := name.(string)
			if seen[key] {
				return nil, fmt.Errorf("overlapping check %s", key)
			}
			seen[key] = true
		}
		encoded, err := yaml.Marshal(copy)
		if err != nil {
			return nil, err
		}
		// Assert preservation on the actual marshalled configuration, not only
		// the in-memory edits. Unknown nested settings must survive unchanged.
		var roundTrip map[string]any
		if err := yaml.Unmarshal(encoded, &roundTrip); err != nil {
			return nil, err
		}
		roundLinters, _ := mapping(roundTrip, "linters")
		roundFormatters, _ := mapping(roundTrip, "formatters")
		roundLinters["enable"], roundFormatters["enable"] = linters["enable"], formatters["enable"]
		if !reflect.DeepEqual(roundTrip, original) {
			return nil, errors.New("partition changed configuration settings")
		}
		result = append(result, partition{group.name, encoded})
	}
	if len(seen) != 5 {
		return nil, errors.New("incomplete lint partition")
	}
	return result, nil
}

func run(ctx context.Context, root, binary string, stdout, stderr io.Writer) (result error) {
	if runtime.GOOS != "linux" {
		return errors.New("lint partition execution requires Ubuntu Linux CI")
	}
	setsid, err := exec.LookPath("setsid")
	if err != nil {
		return err
	}
	kill, err := exec.LookPath("kill")
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(filepath.Join(root, ".golangci.yml"))
	if err != nil {
		return err
	}
	groups, err := partitions(raw)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Root siblings preserve golangci's default cfg-directory path mode.
		// CreateTemp uses exclusive creation and mode0600, never a shared name
		// or a write through an existing symlink.
		file, err := os.CreateTemp(root, ".golangci-shard-*.yml")
		if err != nil {
			return err
		}
		name := file.Name()
		defer func() {
			if err := os.Remove(name); err != nil {
				result = errors.Join(result, err)
			}
		}()
		_, writeErr := file.Write(group.Config)
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "golangci partition %s: ./...; timeout5m; canonical settings preserved\n", group.Name)
		// A normally spawned child inherits our group and is not its leader.
		// setsid therefore execs without forking: its PID is the new PGID.
		// No --fork/--wait or caller-provided process identity is accepted.
		command := exec.CommandContext(ctx, setsid, "--", binary, "run", "--verbose", "--timeout=5m", "--config", name, "./...")
		command.Dir, command.Stdout, command.Stderr = root, stdout, stderr
		command.WaitDelay = 5 * time.Second
		command.Cancel = func() error {
			// Kill only the group derived from our own positive child PID.
			// Stop the leader first: even if setsid has not run yet, it can
			// no longer create a group or start descendants after our sweep.
			if command.Process.Pid <= 1 {
				return errors.New("invalid own lint process identity")
			}
			leaderErr := command.Process.Kill()
			groupErr := exec.Command(kill, "-KILL", "--", "-"+strconv.Itoa(command.Process.Pid)).Run()
			if leaderErr == nil || groupErr == nil || errors.Is(leaderErr, os.ErrProcessDone) {
				return nil
			}
			return errors.Join(leaderErr, groupErr)
		}
		if err := command.Run(); err != nil {
			return fmt.Errorf("%s partition: %w", group.Name, err)
		}
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: golangci-shards /path/to/golangci-lint")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, ".", os.Args[1], os.Stdout, os.Stderr)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
