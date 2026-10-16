package slidesexport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The child emulates only this test's owned Docker CLI responses. It has no
// Docker socket and never invokes a daemon; actual-image qualification is CI.
func TestRunnerOwnedProcessLifecycle(t *testing.T) {
	if os.Getenv("CREWSHIP_TEST_EXPORT_PROCESS_CHILD") == "1" {
		exportProcessFixture()
		os.Exit(0)
	}
	for _, mode := range []string{"normal", "cancel_create", "foreign", "cancel_start", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nexec \"$CREWSHIP_TEST_EXPORT_EXE\" -test.run='^TestRunnerOwnedProcessLifecycle$' -- \"$@\"\n"
			if err = os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CREWSHIP_TEST_EXPORT_EXE", exe)
			t.Setenv("CREWSHIP_TEST_EXPORT_PROCESS_CHILD", "1")
			t.Setenv("CREWSHIP_TEST_EXPORT_PROCESS_DIR", dir)
			t.Setenv("CREWSHIP_TEST_EXPORT_PROCESS_MODE", mode)
			raw, err := os.ReadFile("../../tools/slides-export/testdata/supported.json")
			if err != nil {
				t.Fatal(err)
			}
			input, err := Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			b := Binding{Mode: "report", PageID: input.Deck.DeckID, Revision: input.Revision, ContentHash: input.ContentHash}
			if err = os.WriteFile(filepath.Join(dir, "frame"), fixtureFrame(t, b, nil), 0600); err != nil {
				t.Fatal(err)
			}
			r := &Runner{Image: "sha256:" + strings.Repeat("a", 64), OwnerID: "unit", JournalDir: filepath.Join(dir, "journal")}
			defer r.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "cancel_create" || mode == "cancel_start" {
				go func() {
					marker := "create-entered"
					if mode == "cancel_start" {
						marker = "start-entered"
					}
					deadline := time.After(3 * time.Second)
					for {
						if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
							cancel()
							return
						}
						select {
						case <-deadline:
							return
						case <-time.After(5 * time.Millisecond):
						}
					}
				}()
			}
			frame, err := r.Run(ctx, raw, b)
			switch mode {
			case "normal":
				if err != nil || len(frame.Report) == 0 {
					t.Fatal("normal", err)
				}
			case "cancel_create", "cancel_start":
				if !errors.Is(err, context.Canceled) || errors.Is(err, ErrCleanup) {
					t.Fatal("cancel did not settle known ID", err)
				}
			case "trailing":
				if !errors.Is(err, ErrProtocol) {
					t.Fatal("trailing bytes accepted", err)
				}
			case "foreign":
				if _, e := os.Stat(filepath.Join(dir, "start-entered")); !os.IsNotExist(e) {
					t.Fatal("document input sent before acknowledged ownership verification")
				}
				if !errors.Is(err, ErrCleanup) {
					t.Fatal("foreign owner accepted", err)
				}
				intents, e := r.journal.list()
				if e != nil || len(intents) != 1 {
					t.Fatal("failed cleanup not retained", e)
				}
				t.Cleanup(func() { uncertainJobs.Delete(intents[0].Name) })
				if _, e = os.Stat(filepath.Join(dir, "container")); e != nil {
					t.Fatal("foreign container removed", e)
				}
				return
			}
			if _, e := os.Stat(filepath.Join(dir, "container")); !os.IsNotExist(e) {
				t.Fatal("owned container survived", e)
			}
			intents, e := r.journal.list()
			if e != nil || len(intents) != 0 {
				t.Fatal("settled intent retained", e)
			}
			removed, e := os.ReadFile(filepath.Join(dir, "removed"))
			if e != nil || string(removed) != strings.Repeat("c", 64) {
				t.Fatal("cleanup did not use immutable ID", e)
			}
			if mode == "cancel_create" {
				if _, e := os.Stat(filepath.Join(dir, "start-entered")); !os.IsNotExist(e) {
					t.Fatal("started after create cancellation")
				}
			}
		})
	}
}
func exportProcessFixture() {
	dir := os.Getenv("CREWSHIP_TEST_EXPORT_PROCESS_DIR")
	mode := os.Getenv("CREWSHIP_TEST_EXPORT_PROCESS_MODE")
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) < 2 {
		os.Exit(2)
	}
	args = args[1:]
	state := filepath.Join(dir, "container")
	switch args[0] {
	case "create":
		c := ownedContainer{ID: strings.Repeat("c", 64)}
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "--label" {
				if v, ok := strings.CutPrefix(args[i+1], "crewship.slides.export.owner="); ok {
					c.Owner = v
				}
				if v, ok := strings.CutPrefix(args[i+1], "crewship.slides.export.token="); ok {
					c.Token = v
				}
			}
		}
		if mode == "foreign" {
			c.Token = "foreign"
		}
		raw, _ := json.Marshal(c)
		if os.WriteFile(state, raw, 0600) != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(filepath.Join(dir, "create-entered"), nil, 0600)
		if mode == "cancel_create" {
			time.Sleep(150 * time.Millisecond)
		}
		_, _ = os.Stdout.WriteString(c.ID + "\n")
	case "start":
		_ = os.WriteFile(filepath.Join(dir, "start-entered"), nil, 0600)
		if mode == "cancel_start" {
			time.Sleep(10 * time.Second)
		}
		_, _ = io.Copy(io.Discard, os.Stdin)
		raw, err := os.ReadFile(filepath.Join(dir, "frame"))
		if err != nil {
			os.Exit(2)
		}
		_, _ = os.Stdout.Write(raw)
		if mode == "trailing" {
			_, _ = os.Stdout.WriteString("extra")
		}
	case "inspect":
		raw, err := os.ReadFile(state)
		if err != nil {
			_, _ = os.Stderr.WriteString("No such object\n")
			os.Exit(1)
		}
		_, _ = os.Stdout.Write(raw)
	case "rm":
		if len(args) != 3 || args[2] != strings.Repeat("c", 64) {
			os.Exit(2)
		}
		if os.Remove(state) != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(filepath.Join(dir, "removed"), []byte(args[2]), 0600)
	default:
		os.Exit(2)
	}
}
