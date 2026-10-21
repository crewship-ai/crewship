//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/crewship-ai/crewship/internal/sidecar"
)

// Prepare is local, but its machine-readable packet is the input to a later
// protected mutation. Exercise the actual binary and preserve integer identity
// beyond JavaScript's exact range when selecting another output format.
func TestAcceptance_DocumentDraftPrepareFormatsBinary(t *testing.T) {
	binary := buildCrewshipBinary(t)
	const revision int64 = 9007199254740993
	input := strings.Replace(draftPrepareFixture, `"rev":0`, `"rev":9007199254740993`, 1)
	for _, format := range []string{"default", "json", "ndjson", "yaml"} {
		t.Run(format, func(t *testing.T) {
			args := []string{"doc", "draft", "prepare", "--file", "-"}
			if format != "default" {
				args = append(args, "--format", format)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, binary, args...)
			command.Dir = t.TempDir()
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "CREWSHIP_") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env, "CREWSHIP_AGENT_TOKEN=agtv2.aaaa", "CREWSHIP_NO_SLUG_CACHE=1", "NO_COLOR=1")
			command.Stdin = strings.NewReader(input)
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := command.Run(); err != nil {
				t.Fatalf("actual draft prepare failed: %v (stderr bytes=%d)", err, stderr.Len())
			}
			raw := stdout.Bytes()
			if format == "yaml" {
				if json.Valid(raw) {
					t.Fatal("--format yaml returned JSON instead of a YAML mapping")
				}
				var value map[string]any
				if err := yaml.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				var err error
				raw, err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
			} else if format == "ndjson" && bytes.Count(raw, []byte{'\n'}) != 1 {
				t.Fatal("NDJSON packet must be one complete record")
			}
			var result struct {
				Status string                      `json:"status"`
				Packet sidecar.DocumentDraftPacket `json:"packet"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status != "prepared_unsaved" || result.Packet.Patch.IfRev != revision || result.Packet.Checkpoint.IfRev != revision+1 || result.Packet.Generation != 1 || result.Packet.Patch.ID != "draft-one" || result.Packet.Checkpoint.ID != "draft-one-checkpoint" || result.Packet.Patch.PageID != "own-page" {
				t.Fatal("rendering changed the immutable packet identity, revision or unsaved status")
			}
		})
	}
}
