//go:build !clionly

package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// explicitServerTarget must answer "did the operator name a server?", which
// cli.EffectiveServer cannot: it manufactures http://localhost:8080 out of
// nothing when the CLI is unconfigured. A gate built on EffectiveServer would
// refuse every invocation on a bare machine — and, worse, would report a
// server URL nobody ever typed as the reason.
func TestExplicitServerTarget(t *testing.T) {
	saveCLIState(t)

	tests := []struct {
		name       string
		flagServer string
		env        string
		cfg        *cli.CLIConfig
		wantURL    string
		wantOrigin string
		// wantNamed is separate from wantURL because they came apart: a
		// selected-but-serverless profile names a target that resolves to no
		// URL, and folding "no URL" into "not named" is the bug below.
		wantNamed bool
	}{
		{
			name: "nothing configured is not a target",
			cfg:  &cli.CLIConfig{},
		},
		{
			name: "nil config is not a target",
		},
		{
			name:       "--server wins",
			flagServer: "https://flag.example",
			env:        "https://env.example",
			cfg:        &cli.CLIConfig{Server: "https://cfg.example"},
			wantURL:    "https://flag.example",
			wantOrigin: "--server",
		},
		{
			name:       "CREWSHIP_SERVER beats the config file",
			env:        "https://env.example",
			cfg:        &cli.CLIConfig{Server: "https://cfg.example"},
			wantURL:    "https://env.example",
			wantOrigin: "CREWSHIP_SERVER",
		},
		{
			name:       "a logged-in config counts",
			cfg:        &cli.CLIConfig{Server: "https://cfg.example"},
			wantURL:    "https://cfg.example",
			wantOrigin: "the CLI config",
		},
		{
			// Loopback is a server like any other. Treating it as "must be my
			// own data dir" is the inference that hid #2086.
			name:       "loopback is still a named target",
			env:        "http://localhost:8083",
			wantURL:    "http://localhost:8083",
			wantOrigin: "CREWSHIP_SERVER",
		},
		{
			name: "a selected profile with a server",
			cfg: &cli.CLIConfig{
				Current: "prod",
				Servers: map[string]*cli.ServerProfile{"prod": {Server: "https://prod.example"}},
			},
			wantURL:    "https://prod.example",
			wantOrigin: `profile "prod"`,
		},
		{
			// A selected profile is authoritative — it does not fall through
			// to the env or the legacy field, matching cli.EffectiveServer.
			// But unlike EffectiveServer it must still count as a NAMED
			// target: EffectiveServer's "" fails closed for dialling, whereas
			// here the same "" would fail OPEN and let the command answer from
			// the local file. See the two cases below.
			name: "a serverless profile is still a named target",
			env:  "https://env.example",
			cfg: &cli.CLIConfig{
				Current: "prod",
				Servers: map[string]*cli.ServerProfile{"prod": {}},
				Server:  "https://cfg.example",
			},
			wantURL:    "",
			wantOrigin: `profile "prod"`,
			wantNamed:  true,
		},
		{
			name: "an undefined profile is a named target too",
			env:  "https://env.example",
			cfg: &cli.CLIConfig{
				Current: "typo",
				Servers: map[string]*cli.ServerProfile{"prod": {Server: "https://prod.example"}},
				Server:  "https://cfg.example",
			},
			wantURL:    "",
			wantOrigin: `profile "typo"`,
			wantNamed:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", tt.env)
			t.Setenv("CREWSHIP_PROFILE", "")
			flagServer, flagProfile, cliCfg = tt.flagServer, "", tt.cfg

			got, named := explicitServerTarget()
			wantNamed := tt.wantNamed || tt.wantURL != ""
			if named != wantNamed {
				t.Fatalf("named = %v, want %v (target %+v)", named, wantNamed, got)
			}
			if !wantNamed {
				return
			}
			if got.URL != tt.wantURL {
				t.Errorf("URL = %q, want %q", got.URL, tt.wantURL)
			}
			if !strings.Contains(got.Origin, tt.wantOrigin) {
				t.Errorf("Origin = %q, want it to name %q", got.Origin, tt.wantOrigin)
			}
			// Whatever the shape, the description has to be readable — an
			// empty URL must not render as "targets  ()".
			if d := got.describe(); strings.TrimSpace(d) == "" || strings.Contains(d, "()") {
				t.Errorf("describe() = %q, which is not a sentence a human can act on", d)
			}
		})
	}
}

// The path in a refusal is read by a human and pasted into `ls`. A DSN with a
// scheme and pragmas is neither.
func TestDSNFilePath(t *testing.T) {
	tests := []struct{ dsn, want string }{
		{"file:./crewship.db", "./crewship.db"},
		{"file:/srv/crewship/crewship_3/crewship.db", "/srv/crewship/crewship_3/crewship.db"},
		{"file://" + "/abs/crewship.db", "/abs/crewship.db"},
		{"/plain/path.db", "/plain/path.db"},
		{"file:./crewship.db?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)", "./crewship.db"},
		{"  file:/spaced.db  ", "/spaced.db"},
	}
	for _, tt := range tests {
		if got := dsnFilePath(tt.dsn); got != tt.want {
			t.Errorf("dsnFilePath(%q) = %q, want %q", tt.dsn, got, tt.want)
		}
	}
}

// DATABASE_URL says WHICH file. It does not say WHOSE, so it is not consent to
// ignore a named server — that is what --local is for. A DATABASE_URL that
// silently satisfied the gate would re-open the hole for exactly the
// population most likely to hit it: operators on a dev clone.
func TestResolveLocalDBTarget_DatabaseURLWinsButIsNotConsent(t *testing.T) {
	saveCLIState(t)
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	t.Setenv("DATABASE_URL", "file:/somewhere/else/crewship.db?_pragma=foo(1)")

	target, err := resolveLocalDBTarget()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if target.Path != "/somewhere/else/crewship.db" {
		t.Errorf("Path = %q, want the DATABASE_URL file", target.Path)
	}
	if target.Origin != "DATABASE_URL" {
		t.Errorf("Origin = %q, want DATABASE_URL", target.Origin)
	}

	cliCfg = &cli.CLIConfig{Server: "https://prod.example"}
	flagServer, flagProfile = "", ""
	c := &cobra.Command{Use: "x"}
	c.Flags().Bool("local", false, "")
	if _, err := requireLocalDB(c, "crewship db migration-status", ""); err == nil {
		t.Fatal("DATABASE_URL alone got past the gate")
	}
}

// With nothing naming a server, "the local file" is the only reading, so the
// gate must not block — but it still has to say which file it picked.
func TestRequireLocalDB_UnambiguousStillNamesTheFile(t *testing.T) {
	saveCLIState(t)
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	t.Setenv("DATABASE_URL", "")
	cliCfg = &cli.CLIConfig{}
	flagServer, flagProfile = "", ""

	c := &cobra.Command{Use: "x"}
	c.Flags().Bool("local", false, "")

	var target localDBTarget
	out, err := covCaptureStderrCli6(t, func() error {
		var e error
		target, e = requireLocalDB(c, "crewship admin promote", "")
		return e
	})
	if err != nil {
		t.Fatalf("refused an unambiguous invocation: %v", err)
	}
	want := filepath.Join(dir, "crewship.db")
	if target.Path != want {
		t.Errorf("Path = %q, want %q", target.Path, want)
	}
	if !strings.Contains(out, want) {
		t.Errorf("stderr does not name the database it resolved:\n%s", out)
	}
}

// The refusal has to be usable: it names the server, the file, and the flag.
// A "wrong database" error that does not say which two databases it means
// sends the operator to read source.
func TestRequireLocalDB_RefusalIsActionable(t *testing.T) {
	saveCLIState(t)
	dir := t.TempDir()
	t.Setenv("CREWSHIP_DATA_DIR", dir)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("CREWSHIP_SERVER", "http://localhost:8083")
	cliCfg = &cli.CLIConfig{}
	flagServer, flagProfile = "", ""

	c := &cobra.Command{Use: "x"}
	c.Flags().Bool("local", false, "")

	_, err := requireLocalDB(c, "crewship admin promote", "crewship workspace member role <id> OWNER")
	if err != nil {
		for _, want := range []string{
			"http://localhost:8083",
			"CREWSHIP_SERVER",
			filepath.Join(dir, "crewship.db"),
			"--local",
			"crewship workspace member role",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal does not mention %q:\n%v", want, err)
			}
		}
	} else {
		t.Fatal("targeting a server did not refuse")
	}

	// The exit code is the machine-readable half of "you asked for something
	// contradictory". Exit 0 on this path is the whole bug.
	if code := cli.ExitCodeFor(err); code == 0 {
		t.Errorf("exit code %d, want non-zero", code)
	}
}

// localDBGateCallSite matches the gate's two entry points and captures the
// command name each one announces itself as.
var localDBGateCallSite = regexp.MustCompile(`(?:requireLocalDB|openGatedLocalDB)\(cmd,\s*"([^"]+)"`)

// Every command behind the gate must have a way past it.
//
// This is the guard for the bug this file's own author shipped and caught by
// re-reading the diff: `keeper eval` was routed through requireLocalDB without
// declaring --local, so on any machine with a configured server it refused
// with an instruction the operator could not follow. A refusal that names an
// impossible escape is worse than no gate — it is a command that simply cannot
// run, presented as a user error.
//
// The check is source-derived on purpose. Nothing about "is this command
// gated?" is visible on the *Command value, so a test that enumerated the tree
// could only assert on commands it already knew about — which is how a guard
// ends up seeing 60% of its call sites (#2086's own headline). Reading the
// call sites means a new one is covered the moment it is written.
func TestLocalDBGate_EveryGatedCommandCanOptIn(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	seen := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range localDBGateCallSite.FindAllStringSubmatch(string(src), -1) {
			announced := m[1]
			seen++

			// "crewship memory log --local" → ["memory", "log"]
			path := strings.Fields(announced)
			if len(path) == 0 || path[0] != "crewship" {
				t.Errorf("%s: gate call announces itself as %q, which does not start with \"crewship\" — "+
					"the operator has to be able to match the refusal to what they typed", name, announced)
				continue
			}
			var args []string
			for _, tok := range path[1:] {
				if strings.HasPrefix(tok, "-") {
					continue
				}
				args = append(args, tok)
			}

			cmd, _, err := rootCmd.Find(args)
			if err != nil || cmd == nil || cmd == rootCmd {
				t.Errorf("%s: gate call names %q, which resolves to no command", name, announced)
				continue
			}
			if cmd.Flags().Lookup("local") == nil && !hasInheritedLocalFlag(cmd) {
				t.Errorf("%s: `%s` is gated by requireLocalDB but declares no --local, "+
					"so the refusal it prints names an escape hatch that does not exist",
					name, announced)
			}
		}
	}

	// The regex is the weak link: a refactor that renames the helpers or moves
	// the command name off the first argument would make this test pass by
	// finding nothing. Pin the floor to the call sites that exist today.
	if seen < 9 {
		t.Errorf("found only %d gate call sites; the matcher has probably stopped seeing them", seen)
	}
}

// …and the converse: no command may ADVERTISE --local unless it honours it.
//
// The first draft declared the flag persistently on `adminCmd`, which also
// hosts eight HTTP-only verbs (stats, health, gdpr, prune-legacy, reencrypt,
// reap-orphan-containers, ratelimits, memory-config). `crewship admin stats
// --help` listed "--local  Act on the database FILE on this host", the flag was
// accepted, and the command went to the server anyway. Advertising a switch
// that does nothing is the same category of lie this whole PR is about, and it
// is exactly what cmd_memory_versions.go's own comment argues against.
func TestLocalDBGate_NoCommandAdvertisesALocalFlagItIgnores(t *testing.T) {
	gated := map[string]bool{}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, m := range localDBGateCallSite.FindAllStringSubmatch(string(src), -1) {
			var args []string
			for _, tok := range strings.Fields(m[1])[1:] {
				if !strings.HasPrefix(tok, "-") {
					args = append(args, tok)
				}
			}
			if cmd, _, err := rootCmd.Find(args); err == nil && cmd != nil {
				gated[cmd.CommandPath()] = true
			}
		}
	}

	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		// Runnable commands only: a group like `admin` or `db` carries the
		// persistent declaration but never executes anything itself.
		if c.Runnable() && (c.Flags().Lookup("local") != nil || hasInheritedLocalFlag(c)) && !gated[c.CommandPath()] {
			t.Errorf("`%s` advertises --local but no gate call site names it — "+
				"the flag will be accepted and silently ignored", c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
}

// hasInheritedLocalFlag walks the parents for a persistent --local, which is
// how `admin` and `db` declare it once for a whole group.
func hasInheritedLocalFlag(cmd *cobra.Command) bool {
	for c := cmd.Parent(); c != nil; c = c.Parent() {
		if c.PersistentFlags().Lookup("local") != nil {
			return true
		}
	}
	return false
}

// A persistent --local declared on the group command must be visible to a
// subcommand's RunE. cobra folds inherited flags into Flags() during
// ParseFlags, which does not run when a test (or any direct caller) invokes
// RunE — so a gate that only consulted cmd.Flags() would read "false" in
// exactly the place it is most often exercised.
func TestLocalOnlyFlag_SeesAPersistentParentFlag(t *testing.T) {
	parent := &cobra.Command{Use: "parent"}
	parent.PersistentFlags().Bool("local", false, "")
	child := &cobra.Command{Use: "child"}
	parent.AddCommand(child)

	if localOnlyFlag(child) {
		t.Fatal("--local read as set before anything set it")
	}
	if err := parent.PersistentFlags().Set("local", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if !localOnlyFlag(child) {
		t.Error("a persistent --local on the parent was invisible to the child")
	}

	// A command that never declared the flag is "not passed", not an error.
	orphan := &cobra.Command{Use: "orphan"}
	if localOnlyFlag(orphan) {
		t.Error("an undeclared --local read as set")
	}
	if localOnlyFlag(nil) {
		t.Error("nil command read as --local")
	}
}
