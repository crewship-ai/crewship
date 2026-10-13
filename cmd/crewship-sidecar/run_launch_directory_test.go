package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/runoutput"
)

func TestLaunchRefusesAmbientWorkloadDirectoryBeforeClaim(t *testing.T) {
	for _, workDir := range []string{"", "relative"} {
		t.Run(workDir, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "unclaimed")
			spec := launchInput{Command: runoutput.Command{Args: []string{"sh", "-c", "exit 0"}, Dir: workDir}, Deadline: time.Now().Add(time.Minute)}
			input, err := json.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			if code := runLaunchCommand(t.Context(), "run-launch", []string{"--dir", dir, "--session", "fixture"}, bytes.NewReader(input), &out, &diagnostics); code != 125 {
				t.Fatalf("invalid directory admitted: code=%d diagnostics=%s", code, diagnostics.String())
			}
			if _, err := os.Stat(dir); !os.IsNotExist(err) {
				t.Fatalf("invalid admission claimed launch identity: %v", err)
			}
		})
	}
}
