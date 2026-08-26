//go:build !clionly

package main

// local_db_target.go — the one place that decides whether a command may read
// or write the SQLite file on THIS host.
//
// The defect it exists for (#2086, Critical 2): `openAdminDB()` resolved
// ~/.crewship/crewship.db and never looked at --server / CREWSHIP_SERVER /
// --profile, while every dev clone starts its server with
// DATABASE_URL=file:./crewship.db. So `crewship admin list-users` pointed at a
// populated server printed
//
//	(no users — run `crewship seed` or hit POST /api/v1/bootstrap)
//
// and exited 0: a confident wrong answer about a server it never contacted.
// The commands with an HTTP route are rewired to use it. What is left here are
// the commands that genuinely have no route — recovery tools that must work
// with the server down — and for those the rule is:
//
//	Name the file. Refuse when the operator has named a server, because
//	"the file I found" and "the server you named" are two different targets
//	and only the operator knows whether they are the same database.
//
// `--local` is that statement of intent. It is deliberately the ONLY way past
// the gate: a loopback exemption is what hid this bug for so long — the CLI
// used to treat "the server is on localhost" as "the server uses my default
// data dir", which is false for every dev clone, every container, and every
// multi-instance host.

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
)

// serverTarget is a server the operator has explicitly named, and where they
// named it. The origin goes into the refusal so they know which knob to turn.
type serverTarget struct {
	URL    string
	Origin string
}

// explicitServerTarget reports the server the operator actually named, if any.
//
// This is NOT cli.EffectiveServer: that one falls back to the hardcoded
// "http://localhost:8080" when nothing is configured, so it can never answer
// "no server was named" — which is precisely the question the gate asks. The
// precedence order mirrors EffectiveServer so the gate refuses on the same URL
// the HTTP commands would have dialled.
func explicitServerTarget() (serverTarget, bool) {
	if s := strings.TrimSpace(flagServer); s != "" {
		return serverTarget{URL: s, Origin: "--server"}, true
	}
	// A selected profile is authoritative, exactly as in EffectiveServer: a
	// profile with no server does NOT fall through to the env or the legacy
	// top-level field, so neither does the gate.
	if name, p := cliCfg.ActiveProfile(flagProfile); name != "" {
		if p != nil && strings.TrimSpace(p.Server) != "" {
			return serverTarget{URL: p.Server, Origin: fmt.Sprintf("profile %q", name)}, true
		}
		return serverTarget{}, false
	}
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_SERVER")); v != "" {
		return serverTarget{URL: v, Origin: "CREWSHIP_SERVER"}, true
	}
	if cliCfg != nil && strings.TrimSpace(cliCfg.Server) != "" {
		return serverTarget{URL: cliCfg.Server, Origin: "the CLI config (`crewship login`)"}, true
	}
	return serverTarget{}, false
}

// localDBTarget is the database file a local-only command would operate on,
// and how it was resolved. DSN is what database.Open wants; Path is the plain
// filesystem path, for the commands that need the file itself (snapshots live
// beside it) and for every message printed to a human.
type localDBTarget struct {
	DSN    string
	Path   string
	Origin string
}

// resolveLocalDBTarget resolves the local database without opening it.
//
// DATABASE_URL wins, matching the server's own resolution and the documented
// escape hatch for a non-default location. Note that honouring it is NOT
// consent to ignore a named server: it says which file, not whose.
func resolveLocalDBTarget() (localDBTarget, error) {
	if dsn := strings.TrimSpace(os.Getenv("DATABASE_URL")); dsn != "" {
		return localDBTarget{DSN: dsn, Path: dsnFilePath(dsn), Origin: "DATABASE_URL"}, nil
	}
	// ResolveDefaultDataDir, not DefaultDataDir: resolving a path in order to
	// print it must not create a data directory tree as a side effect.
	dd, err := database.ResolveDefaultDataDir()
	if err != nil {
		return localDBTarget{}, fmt.Errorf("resolve data dir: %w", err)
	}
	return localDBTarget{DSN: dd.DatabaseURL(), Path: dd.DatabasePath(), Origin: "the default data directory"}, nil
}

// dsnFilePath strips a SQLite DSN down to the file it names, so a message to a
// human reads "/srv/crewship/crewship_3/crewship.db" and not
// "file:./crewship.db?_pragma=busy_timeout(30000)". Mirrors what
// internal/database.parseDSN does with the prefix, plus the query string that
// Open appends afterwards.
func dsnFilePath(dsn string) string {
	p := strings.TrimPrefix(strings.TrimSpace(dsn), "file:")
	p = strings.TrimPrefix(p, "//")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	return p
}

// localOnlyFlag reports whether --local was passed.
//
// It walks the parents rather than trusting cmd.Flags(), because a persistent
// flag declared on `admin` / `db` is folded into a subcommand's Flags() by
// ParseFlags — which runs under Execute and NOT when a test calls RunE
// directly. A gate that silently reads "false" in the ~30 places this package
// invokes RunE by hand would be untestable at exactly the layer it matters.
//
// Lookup-then-read rather than a bare GetBool for the same reason in reverse:
// several tests build a bare cobra.Command around a production RunE with only
// the flags that case needs, and a command that never declared --local must
// read as "not passed", not as an error.
func localOnlyFlag(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags()} {
			f := fs.Lookup("local")
			if f == nil {
				continue
			}
			if v, err := strconv.ParseBool(f.Value.String()); err == nil && v {
				return true
			}
		}
	}
	return false
}

// localOnlyFlagHelp is the one wording for --local, so `admin`, `db`, `memory`
// and `keeper eval` cannot drift into describing the same gate three ways.
const localOnlyFlagHelp = "Act on the database FILE on this host, not the server the CLI targets"

// requireLocalDB is the gate. It returns the database this command may touch,
// or an error explaining why the invocation is contradictory.
//
// "what" is the command as the operator typed it ("crewship admin promote").
// "alternative" is one line naming the server-side way to do this, or "" when
// there isn't one — the difference between a refusal that teaches and one that
// just blocks.
func requireLocalDB(cmd *cobra.Command, what, alternative string) (localDBTarget, error) {
	target, err := resolveLocalDBTarget()
	if err != nil {
		return localDBTarget{}, err
	}

	srv, named := explicitServerTarget()
	if !named {
		// Nothing names a server, so "the local file" is the only thing this
		// invocation can mean. Still say which file: a command that answers
		// from a database without naming it is how the wrong one goes
		// unnoticed for three months.
		fmt.Fprintf(os.Stderr, "note: %s reads the local database at %s (%s)\n",
			what, target.Path, target.Origin)
		return target, nil
	}

	if localOnlyFlag(cmd) {
		fmt.Fprintf(os.Stderr,
			"note: --local: using the database file at %s (%s), NOT the server at %s\n",
			target.Path, target.Origin, srv.URL)
		return target, nil
	}

	msg := fmt.Sprintf(
		"%s can only work on the database FILE on this host — %s (%s) — but your CLI targets %s (%s).\n"+
			"Those are not the same thing, and this command cannot tell whether that server uses that file.\n\n"+
			"  • to act on the local file, say so:  add --local\n",
		what, target.Path, target.Origin, srv.URL, srv.Origin)
	if alternative != "" {
		msg += "  • to act on the server:              " + alternative + "\n"
	}
	return localDBTarget{}, cli.WithExitCode(fmt.Errorf("%s", msg), cli.ExitValidation)
}

// openGatedLocalDB is requireLocalDB plus the open, for the commands that want a
// *database.DB. Callers close it.
func openGatedLocalDB(cmd *cobra.Command, what, alternative string) (*database.DB, error) {
	target, err := requireLocalDB(cmd, what, alternative)
	if err != nil {
		return nil, err
	}
	// Only ENOENT means "not initialised" — permission denied, an I/O error or
	// a symlink loop must surface verbatim instead of being mis-reported as a
	// missing database, or the operator chases `crewship init` when the real
	// problem is access rights.
	if _, statErr := os.Stat(target.Path); statErr != nil {
		if os.IsNotExist(statErr) {
			return nil, cli.NotFoundf("database not found at %s — set DATABASE_URL or run `crewship init` first", target.Path)
		}
		return nil, fmt.Errorf("stat database path %s: %w", target.Path, statErr)
	}
	return database.Open(target.DSN)
}
