package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
)

// clistate_test.go — one contract for the process-wide CLI state these tests
// share.
//
// The problem it exists for
// -------------------------
// `crewship` is a Cobra program, and Cobra binds flags to package-level
// variables: --format writes flagFormat, --server writes flagServer, and the
// root PersistentPreRun blanks the whole colour palette in internal/cli when
// stdout is not a terminal (which it never is under `go test`). Production
// gets away with that because a CLI process handles exactly one command and
// then exits. A test binary runs ~3,000 of them in one process, so whatever
// the last test left in those variables is what the next test starts from.
//
// In fixed source order that is stable and therefore invisible. Under
// -shuffle=on it is not: a test asserting on a table reads another test's
// leftover flagFormat="json" and fails on output it never asked for, and
// TestSeverityColor fails because some earlier test ran a command through
// Cobra and the palette has been empty ever since. CI (#1548) counted 58
// failing top-level tests on one seed and 28 on the next; a local run on seed
// 1785429252149193000 failed 15. The number moves with the seed and the
// failing test is never the guilty one, which is the whole difficulty.
//
// The contract
// ------------
// Every test starts from the state the process starts from. That is enforced
// by RESETTING to the process defaults when a test ends, not by restoring
// whatever the value happened to be when the test began: a restore-to-entry
// snapshot faithfully preserves the previous test's pollution, and it has to
// be taken before the mutation, which forces the guard to the top of every
// test. A reset can be registered from anywhere — including from the shared
// fixture helpers below, which is why ~30 helper call sites cover all 224
// tests that touch these globals.
//
// Why not delete the globals instead. flagFormat and its neighbours are
// Cobra's binding targets (`rootCmd.PersistentFlags().StringVarP(&flagFormat,
// …)`) and cli.Reset/Red/… are the palette every printer in internal/cli
// writes through. Both are production structure, not a testing shortcut, so
// this is a test-side fix by design.

// pristineColors holds internal/cli's colour palette as it was before any test
// ran, so the reset does not have to hardcode a second copy of the escape codes
// that would rot the day a colour changes.
var pristineColors struct {
	reset, bold, dim, red, green, yellow, blue, magenta, cyan, white, gray string
}

// pristineFlag is one Cobra flag's start-of-process state.
//
// Both halves matter. The value is obvious; Changed is the subtle one, because
// ~144 places in this package's production code branch on
// cmd.Flags().Changed(name) to distinguish "the user passed --ttl=0" from "the
// user did not pass --ttl at all". A test that puts a flag back with
// Flags().Set(name, "0") restores the value and sets Changed to TRUE, so the
// flag reads as explicitly supplied forever after. That is how
// TestHireRunECov_AcceptedPendingReview — which asserts ttl_minutes is OMITTED
// from the request body — fails whenever any earlier test touched hireCmd's
// --ttl.
type pristineFlag struct {
	flag    *pflag.Flag
	value   string
	slice   []string
	isSlice bool
	changed bool
}

var pristineFlags []pristineFlag

// snapshotPristineCLIState records the state every test is entitled to start
// from. TestMain calls it, which is the earliest point where the command tree
// is complete: Cobra assembles it in init() functions, and package variable
// initialisation runs before those, so a `var pristine = snapshot()` would
// capture a half-built tree.
func snapshotPristineCLIState() {
	pristineColors.reset, pristineColors.bold, pristineColors.dim = cli.Reset, cli.Bold, cli.Dim
	pristineColors.red, pristineColors.green, pristineColors.yellow = cli.Red, cli.Green, cli.Yellow
	pristineColors.blue, pristineColors.magenta, pristineColors.cyan = cli.Blue, cli.Magenta, cli.Cyan
	pristineColors.white, pristineColors.gray = cli.White, cli.Gray

	seen := map[*pflag.Flag]bool{}
	record := func(f *pflag.Flag) {
		if seen[f] {
			return // persistent flags are visible from every descendant
		}
		seen[f] = true
		p := pristineFlag{flag: f, changed: f.Changed}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			// Slice values append on Set once they have been written, so they
			// have to go back through Replace or a reset would concatenate.
			p.isSlice = true
			p.slice = append([]string(nil), sv.GetSlice()...)
		} else {
			p.value = f.Value.String()
		}
		pristineFlags = append(pristineFlags, p)
	}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		c.Flags().VisitAll(record)
		c.PersistentFlags().VisitAll(record)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// resetCLIState returns every process-wide CLI variable to its start-of-process
// value.
func resetCLIState() {
	cliCfg = nil
	flagServer = ""
	flagWorkspace = ""
	flagFormat = ""
	flagProfile = ""
	flagVerbose = false
	flagNoColor = false
	flagAllowServerMismatch = false

	cli.Reset, cli.Bold, cli.Dim = pristineColors.reset, pristineColors.bold, pristineColors.dim
	cli.Red, cli.Green, cli.Yellow = pristineColors.red, pristineColors.green, pristineColors.yellow
	cli.Blue, cli.Magenta, cli.Cyan = pristineColors.blue, pristineColors.magenta, pristineColors.cyan
	cli.White, cli.Gray = pristineColors.white, pristineColors.gray

	for _, p := range pristineFlags {
		if p.isSlice {
			_ = p.flag.Value.(pflag.SliceValue).Replace(p.slice)
		} else if p.flag.Value.String() != p.value {
			_ = p.flag.Value.Set(p.value)
		}
		p.flag.Changed = p.changed
	}
}

// guardCLIState registers resetCLIState to run when the top-level test
// finishes.
//
// Register it as early as the fixture allows: t.Cleanup runs last-added
// first, so an early registration means the reset runs LAST and gets the final
// word over any restore-to-snapshot cleanup a test also installed.
//
// Calling it more than once in a test is harmless — the reset is idempotent —
// which is what makes it safe to put inside shared helpers rather than
// auditing three thousand test functions.
//
// CALL IT FROM THE TOP-LEVEL TEST FUNCTION, not from inside a t.Run body.
// Because of the carve-out below this is a silent no-op when handed a
// subtest's *testing.T, and that includes the indirect route: covSetupCLI,
// saveCLIState, covStub, covResetFlags and the covCapture* helpers all call it
// with whatever t they were given, so a test whose ONLY fixture calls happen
// inside its subtests registers no reset at all and leaks for the rest of the
// process. That is #2027 — TestCrewFilesListRunE dirtied crewFilesListCmd's
// --path/--recursive/--filter and TestCLIStateIsPristine failed on any seed
// that ordered it later. A guard at the top of the parent costs nothing when
// a fixture has already registered one, so add it whenever the test mutates
// shared CLI state, even if a helper looks like it has you covered.
//
// Subtests are deliberately skipped. The unit of isolation here is the
// top-level test, because that is the unit -shuffle=on reorders; a subtest's
// cleanup fires while its siblings still have to run, and wiping the state its
// parent set up for all of them breaks tests that were fine. TestChatReactListCmd
// is the live example: the parent builds cliCfg once via covStub and three
// subtests share it. Whatever a subtest leaves behind is cleared when the parent
// ends, which is the guarantee the next top-level test actually needs.
func guardCLIState(t *testing.T) {
	t.Helper()
	if strings.Contains(t.Name(), "/") {
		return // subtest: the parent owns the reset
	}
	t.Cleanup(resetCLIState)
}

// TestCLIStateIsPristine is the tripwire for the contract above.
//
// It asserts nothing about its own behaviour — it asserts that whatever ran
// before it left the shared state alone. In source order it always passes and
// proves nothing; under -shuffle=on it lands in a different neighbourhood every
// run, and a leak eventually gets reported HERE, naming the variable, instead
// of surfacing as an unrelated assertion in an unrelated command's test.
func TestCLIStateIsPristine(t *testing.T) {
	if cliCfg != nil {
		t.Errorf("cliCfg leaked from an earlier test: %+v", cliCfg)
	}
	for _, c := range []struct {
		name string
		got  string
	}{
		{"flagServer", flagServer},
		{"flagWorkspace", flagWorkspace},
		{"flagFormat", flagFormat},
		{"flagProfile", flagProfile},
	} {
		if c.got != "" {
			t.Errorf("%s leaked from an earlier test: %q (want empty)", c.name, c.got)
		}
	}
	for _, c := range []struct {
		name string
		got  bool
	}{
		{"flagVerbose", flagVerbose},
		{"flagNoColor", flagNoColor},
		{"flagAllowServerMismatch", flagAllowServerMismatch},
	} {
		if c.got {
			t.Errorf("%s leaked from an earlier test: true (want false)", c.name)
		}
	}
	if cli.Red != pristineColors.red || cli.Reset != pristineColors.reset {
		t.Errorf("internal/cli colour palette leaked from an earlier test "+
			"(Red=%q Reset=%q); a command run through Cobra calls InitColors and "+
			"never puts it back — the fixture that ran it needs guardCLIState",
			cli.Red, cli.Reset)
	}
	if len(pristineFlags) == 0 {
		t.Fatal("no flag snapshot: TestMain must call snapshotPristineCLIState")
	}
	dirty := 0
	for _, p := range pristineFlags {
		if p.flag.Changed != p.changed {
			dirty++
			if dirty <= 5 {
				t.Errorf("--%s Changed=%v (want %v): an earlier test set this flag and "+
					"put the value back with Flags().Set, which leaves Changed=true; "+
					"restore it with a helper that also clears Changed",
					p.flag.Name, p.flag.Changed, p.changed)
			}
			continue
		}
		if !p.isSlice && p.flag.Value.String() != p.value {
			dirty++
			if dirty <= 5 {
				t.Errorf("--%s = %q (want %q): leaked from an earlier test",
					p.flag.Name, p.flag.Value.String(), p.value)
			}
		}
	}
	if dirty > 5 {
		t.Errorf("... and %d more leaked flags", dirty-5)
	}
}
