package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/cobra"
)

// The rule this file enforces: enumerating a shared Cobra command must be a
// READ. Nothing in the parallel phase of this package may write to a shared
// command's child slice.
//
// #1989 is what that costs when it is violated. The mechanism, end to end:
//
//	func (c *Command) Commands() []*Command {
//	    if EnableCommandSorting && !c.commandsAreSorted {
//	        sort.Sort(commandSorterByName(c.commands))  // in-place, shared slice
//	        c.commandsAreSorted = true                  // unsynchronised
//	    }
//	    return c.commands
//	}
//
// So Commands() is a lazy MUTATOR wearing a getter's name, and the guard on
// the mutation is a plain bool. 34 top-level tests in this package call
// t.Parallel() and then enumerate a shared command; if they arrive while
// commandsAreSorted is false, they all sort the same slice at once.
//
// The part that is easy to get wrong — and that made #1989 look narrower
// than it is — is WHEN the bool is false. It is not "once per process,
// until the first enumeration". TestMain already walks the whole tree with
// Commands() (snapshotPristineCLIState, clistate_test.go), which sorts
// everything and sets the bool on every command before m.Run. If that were
// the whole story the race would have been impossible from the start.
//
// Cobra re-dirties it. ExecuteC calls InitDefaultHelpCmd on the ROOT, and
// that function ends with an unconditional pair (cobra@v1.10.2
// command.go:1312-1313):
//
//	c.RemoveCommand(c.helpCommand)   // rebuilds rootCmd.commands wholesale
//	c.AddCommand(c.helpCommand)      // appends, and sets commandsAreSorted = false
//
// initCompleteCmd does the same AddCommand/RemoveCommand dance a few lines
// later. Both run on EVERY execution, not just the first. So any test that
// drives rootCmd.Execute leaves the root unsorted behind it.
//
// Two tests do: runPageCLI (cmd_page_test.go) and TestMain_RunsVersionCommand
// via main() (main_cov2_test.go). Both are serial, so neither races anything
// itself — a serial test's body runs during the sequential pass, while every
// parallel test is still parked. What they do is hand the parallel phase a
// root whose commandsAreSorted is false. Then the parked tests are released
// together and race each other inside sort.Sort. That is the CI stack on
// #1983: two goroutines in commandSorterByName.Swap, reached from
// Commands() in TestPageCLI_IsRegisteredOnRoot.
//
// It also dates the regression. The pages CLI tests introduced the first
// rootCmd.Execute into this package; the 34 parallel enumerators were
// already here and harmless, because nothing was undoing TestMain's sort.
//
// The fix is in TestMain: after the pristine walk has sorted the tree,
// EnableCommandSorting goes false, which makes Commands() a pure read for
// the rest of the process. It is scoped to the test binary — production
// main() still sorts, so user-visible `crewship --help` ordering does not
// move.

// TestCobraSortGuard_SortingIsDisabled fails if the one line in TestMain
// that makes this package safe is removed or reordered before the pristine
// walk. This runs in the plain (non-race) Go job too, so a regression is
// named rather than showing up as a random unrelated flake somewhere else.
func TestCobraSortGuard_SortingIsDisabled(t *testing.T) {
	t.Parallel()

	if cobra.EnableCommandSorting {
		t.Fatal("cobra.EnableCommandSorting is true in the test binary.\n" +
			"Commands() is then a lazy in-place sort behind an unsynchronised bool, " +
			"and every serial rootCmd.Execute re-arms it by re-adding the help command " +
			"(cobra command.go:1312). The 34 parallel tests in this package that " +
			"enumerate a shared command will sort the same slice concurrently (#1989).\n" +
			"Restore `cobra.EnableCommandSorting = false` in TestMain " +
			"(cmd_seed_smoke_cov_test.go), AFTER snapshotPristineCLIState() so the tree " +
			"is already in alphabetical order when sorting is frozen.")
	}
}

// TestCobraSortGuard_ConcurrentEnumerationIsAPureRead is the regression test
// for #1989 rather than a proxy for it: it recreates the exact
// dirty-then-enumerate-in-parallel sequence and lets the race detector judge.
//
// On a tree where sorting is still enabled this is red under -race — the
// goroutines released together all observe commandsAreSorted == false and
// enter sort.Sort on the same backing array. With sorting frozen, Commands()
// writes nothing and there is no race to find.
//
// The name-list comparison at the end is not decoration: it makes the test
// mean something in the plain Go job as well, where concurrent sorts of one
// slice can duplicate or drop an element without any detector running.
//
// Serial by design. It mutates rootCmd (see the InitDefaultHelpCmd call), so
// it must run during the sequential pass, where the parallel tests are parked
// and cannot see the intermediate states.
func TestCobraSortGuard_ConcurrentEnumerationIsAPureRead(t *testing.T) {
	const (
		rounds     = 4
		goroutines = 16
	)

	// Settle the tree before recording the expected enumeration: the first
	// InitDefaultHelpCmd is the one that ADDS `help`, so a snapshot taken
	// before it would be one command short of every later round.
	rootCmd.InitDefaultHelpCmd()
	want := commandNames(rootCmd.Commands())
	if len(want) == 0 {
		t.Fatal("rootCmd has no subcommands — the guard would pass vacuously")
	}

	for round := 0; round < rounds; round++ {
		// Exactly what ExecuteC does to the root on every single run, and
		// the reason TestMain's one-time sort is not enough on its own:
		// RemoveCommand rebuilds rootCmd.commands and AddCommand clears
		// commandsAreSorted. Idempotent in content — the help command is
		// removed and re-added, so the set of children is unchanged.
		rootCmd.InitDefaultHelpCmd()

		var (
			wg    sync.WaitGroup
			start = make(chan struct{})
			seen  = make([][]string, goroutines)
		)
		for i := 0; i < goroutines; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // release everyone at once; staggered starts hide the window
				seen[i] = commandNames(rootCmd.Commands())
			}(i)
		}
		close(start)
		wg.Wait()

		for i, got := range seen {
			if len(got) != len(want) {
				t.Fatalf("round %d, goroutine %d saw %d commands, want %d — "+
					"concurrent enumeration mutated the shared slice (#1989): %v",
					round, i, len(got), len(want), got)
			}
			for j := range got {
				if got[j] != want[j] {
					t.Fatalf("round %d, goroutine %d saw %q at position %d, want %q — "+
						"concurrent enumeration reordered the shared slice (#1989)",
						round, i, got[j], j, want[j])
				}
			}
		}
	}
}

// commandNames snapshots the names of an enumeration so goroutines can be
// compared without sharing the *Command pointers themselves.
func commandNames(cmds []*cobra.Command) []string {
	names := make([]string, len(cmds))
	for i, c := range cmds {
		names[i] = c.Name()
	}
	return names
}

var (
	guardFuncSplit   = regexp.MustCompile(`\nfunc\s`)
	guardTopParallel = regexp.MustCompile(`(?m)^\tt\.Parallel\(\)`)
	guardFuncName    = regexp.MustCompile(`^([A-Za-z0-9_]+)`)
	// A package-level Cobra command is named <thing>Cmd by convention
	// throughout this package (rootCmd, chatCmd, pageCmd, keeperCmd, …).
	// Commands a test builds for itself are locals called cmd, cmd2, root —
	// executing one of those touches no shared state and is not matched.
	// main() is matched because it goes through rootCmd.ExecuteC.
	guardSharedExec = regexp.MustCompile(`\b([A-Za-z0-9_]*Cmd\.Execute(?:C|Context)?|main)\(`)
)

// TestCobraSortGuard_NoParallelTestExecutesASharedCommand covers the half of
// #1989 that freezing the sort does NOT fix.
//
// Disabling EnableCommandSorting stops Commands() from writing. It does not
// stop Execute from writing: ExecuteC still rebuilds rootCmd.commands twice
// per run through RemoveCommand/AddCommand, whatever the sorting setting is.
// Today every caller that drives a shared command is serial, so those writes
// land during the sequential pass and no parallel reader can observe them.
// A new parallel test calling rootCmd.Execute would put that write back in
// the middle of 34 concurrent enumerations — the same failure with a
// different stack, and the blamed test would once again be a random one in a
// package the PR never touched.
//
// Checked in source rather than at runtime for the reason
// clistate_parallel_guard_test.go gives: testing.T does not expose whether a
// test called t.Parallel(), so there is nothing to assert against. The
// pairing is plain in source.
func TestCobraSortGuard_NoParallelTestExecutesASharedCommand(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no test files found — the guard would pass vacuously")
	}

	var offenders []string
	for _, file := range files {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, fn := range guardFuncSplit.Split(string(src), -1)[1:] {
			name := guardFuncName.FindString(fn)
			if !strings.HasPrefix(name, "Test") || !guardTopParallel.MatchString(fn) {
				continue
			}
			if m := guardSharedExec.FindStringSubmatch(stripCommentLines(fn)); m != nil {
				offenders = append(offenders, file+":"+name+" -> "+m[1]+"()")
			}
		}
	}

	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("parallel tests that execute a shared package-level command:\n  %s\n\n"+
			"cobra.ExecuteC rebuilds rootCmd's child slice on every run — "+
			"InitDefaultHelpCmd does RemoveCommand+AddCommand unconditionally, and "+
			"initCompleteCmd does the same. Freezing EnableCommandSorting makes "+
			"Commands() a pure read, but it does not make Execute one, so a parallel "+
			"Execute races the 34 parallel tests that enumerate a shared command "+
			"(#1989).\n"+
			"Drop t.Parallel() from the test above — a serial test's body runs while "+
			"every parallel test is still parked — or build a local command tree and "+
			"execute that instead of the shared singleton (name the local something "+
			"other than <x>Cmd, which is how this guard spots the package-level ones).",
			strings.Join(offenders, "\n  "))
	}
}

// stripCommentLines blanks whole-line // comments so prose describing
// rootCmd.Execute does not read as a call to it. Trailing comments are left
// alone: cutting at the first // would also cut inside string literals such
// as "http://…", which could hide a real call sharing that line.
func stripCommentLines(src string) string {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			lines[i] = ""
		}
	}
	return strings.Join(lines, "\n")
}
