//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/httpsafe"
	"github.com/crewship-ai/crewship/internal/keeper/eval"
	"github.com/crewship-ai/crewship/internal/keepercfg"
	"github.com/crewship-ai/crewship/internal/llm"
)

var (
	flagKeeperEvalEndpoint   string
	flagKeeperEvalIncumbent  string
	flagKeeperEvalCandidates []string
	flagKeeperEvalLimit      int
	flagKeeperEvalPasses     int
	flagKeeperEvalExplain    bool
	flagKeeperEvalTolerance  float64
)

// keeperEvalCmd answers the only question that lets a change to the judge be
// merged: does this model decide the way a person decided?
//
// It is host-side like `crewship db`, not an API call, for two reasons. The
// corpus lives in the local SQLite file (keeper_requests joined against the
// human resolutions in escalations / inbox_items), and the run dials a model
// once per prompt per pass — minutes of work with no natural request boundary.
// Nothing here mutates: the database is opened read-only so a run is safe
// against a live server.
//
// Read the corpus line before the percentages. The harness withholds rates
// entirely below eval.MinHumanRowsForRate human-labelled rows, because the
// failure this whole feature exists to prevent is a confident number computed
// from a label the judge wrote itself.
var keeperEvalCmd = &cobra.Command{
	Use:   "eval",
	Short: "Replay recorded keeper decisions against candidate models and score them against human verdicts",
	Long: `Score candidate judge models on the decisions this instance has already made.

Every prompt the Keeper sent is stored (keeper_requests.ollama_prompt). This
replays them through each candidate model at the production settings and
compares the answers against GROUND TRUTH — what a human decided when they
resolved the escalation — not against what the previous model said.

That distinction is the point. Scoring against the incumbent's own past
decisions measures agreement with a predecessor: a model that is consistently
wrong scores perfectly. Rows nobody ever ruled on are still replayed, but they
are reported in a separate column named for what they are.

Ranking is safety-first. A candidate that downgrades more DENY/ESCALATE
decisions to ALLOW than the incumbent is marked not viable regardless of how
much raw agreement it wins, and so is any candidate on a corpus too small to
conclude from.

Reads the local database directly, read-only, so it is safe to run against a
live server. The models are dialled from THIS machine, so --endpoint must be
reachable from here.

Examples:
  crewship keeper eval --candidate qwen2.5:3b --candidate qwen2.5:7b
  crewship keeper eval --candidate qwen3.5:9b --candidate anthropic/claude-haiku-4-5
  crewship keeper eval --candidate llama3.2:3b --passes 1 --limit 200
  crewship keeper eval --candidate qwen2.5:7b --format json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Read-only, but it ranks judge models from a corpus of past Keeper
		// decisions — so replaying the wrong instance's corpus produces a
		// confident ranking of the wrong thing, at real token cost. Gated
		// like the rest: --local says you mean this machine's history.
		target, err := requireLocalDB(cmd, "crewship keeper eval", "")
		if err != nil {
			return err
		}
		dbPath := target.Path

		// mode=ro: a replay only reads, and the server may well be up holding
		// the file. Opening read-write would take a lock for no reason.
		//
		// mode=ro only takes effect on a "file:" URI — modernc.org/sqlite
		// drops the query string from a bare path and always passes
		// SQLITE_OPEN_CREATE, so the previous `dbPath+"?mode=ro"` was a
		// read-WRITE connection that would happily conjure an empty database
		// for a replay to find nothing in. Stat first so a missing file says
		// so, rather than surfacing as SQLite's "unable to open database
		// file (14)".
		if _, statErr := os.Stat(dbPath); statErr != nil {
			if os.IsNotExist(statErr) {
				return fmt.Errorf("database not found at %s — run `crewship start` first", dbPath)
			}
			return fmt.Errorf("stat %s: %w", dbPath, statErr)
		}
		// Built from the resolved PATH, not from a DSN that may already carry
		// its own query string: "file:x?_pragma=…" + "?mode=ro" is not a URI,
		// and the pragmas are irrelevant to a read-only replay anyway.
		db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
		if err != nil {
			return fmt.Errorf("open %s: %w", dbPath, err)
		}
		defer db.Close()
		ctx := cmdContext(cmd)

		endpoint, incumbent, err := resolveKeeperEvalJudge(ctx, db,
			flagKeeperEvalEndpoint, flagKeeperEvalIncumbent)
		if err != nil {
			return err
		}

		return runKeeperEval(ctx, db, keeperEvalOptions{
			Endpoint:   endpoint,
			Incumbent:  incumbent,
			Candidates: flagKeeperEvalCandidates,
			Limit:      flagKeeperEvalLimit,
			Passes:     flagKeeperEvalPasses,
			Explain:    flagKeeperEvalExplain,
			Tolerance:  flagKeeperEvalTolerance,
			Format:     cli.ResolveFormat(flagFormat, cliCfg),
		}, os.Stdout, os.Stderr)
	},
}

type keeperEvalOptions struct {
	Endpoint   string
	Incumbent  string
	Candidates []string
	Limit      int
	Passes     int
	// Explain prints recorded-vs-replayed per row. Off by default: on a large
	// corpus it is pages of output, and it is a debugging tool rather than part
	// of the verdict.
	Explain   bool
	Tolerance float64
	Format    string
}

// newKeeperEvalProvider builds the llm.Provider a replay dials. It is a
// package-level var so the command can be exercised end to end in a test
// without a model: everything interesting about this command — which label it
// scores against, what it refuses to print — is downstream of the provider, and
// a test that could not reach it would only be testing flag parsing.
var newKeeperEvalProvider = func(endpoint, model string) llm.Provider {
	provider, name := splitCandidateSpec(model)
	if provider != keepercfg.ProviderOllama {
		// Hosted. The key comes from the environment the operator is running in
		// (ANTHROPIC_API_KEY / OPENAI_API_KEY) rather than from the vault: this
		// command reads a local database and dials from this machine, so the
		// instance's stored credentials are not necessarily reachable and
		// borrowing them would be a surprising thing for a read-only tool to do.
		p, err := llm.BuildAuxProviderWithKey(llm.AuxModel{Provider: provider, Model: name}, "", "")
		if err != nil {
			// Surfaced as a provider that fails every call, which the replay
			// already scores as a fail-closed DENY — the same thing an
			// unreachable local endpoint produces, reported the same way.
			return badProvider{err: err}
		}
		return p
	}
	// Same guarded transport the server's judge probe uses. The endpoint is
	// operator-supplied and usually a private address, which TrustedEndpointClient
	// permits while still refusing link-local and other hard-blocked ranges.
	return llm.NewOllamaWithClient(endpoint, name, httpsafe.TrustedEndpointClient(60*time.Second))
}

// splitCandidateSpec reads "provider/model", defaulting to Ollama.
//
// Only the FIRST slash separates, and only when the prefix is a provider we
// know. Ollama tags carry colons and sometimes slashes ("library/llama3"), so a
// naive split would silently re-point an operator's existing local command at a
// paid endpoint — the one mistake this parsing must not make.
func splitCandidateSpec(spec string) (provider, model string) {
	prefix, rest, found := strings.Cut(spec, "/")
	if !found {
		return keepercfg.ProviderOllama, spec
	}
	switch prefix {
	case keepercfg.ProviderOllama, "anthropic", "openai":
		return prefix, rest
	}
	return keepercfg.ProviderOllama, spec
}

// badProvider defers a construction error to call time so a missing API key is
// reported per candidate rather than aborting a run that was scoring three.
type badProvider struct{ err error }

func (b badProvider) Complete(context.Context, llm.Request) (*llm.Response, error) { return nil, b.err }
func (b badProvider) Stream(context.Context, llm.Request, func(llm.StreamEvent) error) (*llm.Response, error) {
	return nil, b.err
}
func (b badProvider) Name() string { return "unavailable" }

// resolveKeeperEvalJudge decides which endpoint and which incumbent model the
// run is measured against: explicit flags first, otherwise whatever the instance
// has configured.
//
// keepercfg.Defaults is deliberately empty. Those defaults are the KEEPER_* env
// / YAML layer, which belongs to the server process, not to this CLI — reading
// this CLI's environment would report a judge the server never uses. So only the
// keeper_runtime_settings row is consulted, and an instance configured purely by
// env resolves to nothing here and is told to pass the flag rather than being
// silently measured against the wrong model.
func resolveKeeperEvalJudge(ctx context.Context, db *sql.DB, endpointFlag, incumbentFlag string) (endpoint, incumbent string, err error) {
	endpoint, incumbent = strings.TrimSpace(endpointFlag), strings.TrimSpace(incumbentFlag)
	if endpoint == "" || incumbent == "" {
		store := keepercfg.New(db, keepercfg.Defaults{})
		if lerr := store.Load(ctx); lerr != nil {
			return "", "", fmt.Errorf("read keeper settings: %w", lerr)
		}
		eff := store.Effective()
		if endpoint == "" {
			endpoint = eff.EndpointURL.Value
		}
		if incumbent == "" {
			incumbent = eff.Model.Value
		}
	}
	if endpoint == "" {
		return "", "", fmt.Errorf("no judge endpoint: pass --endpoint, or set one with 'crewship keeper config set --judge-endpoint'")
	}
	if incumbent == "" {
		return "", "", fmt.Errorf("no incumbent model to compare against: pass --incumbent, or set one with 'crewship keeper config set --judge-model'")
	}
	return endpoint, incumbent, nil
}

// runKeeperEval loads the corpus, replays the incumbent and every candidate over
// it, and renders the comparison. Split from RunE so a test can drive it with an
// in-memory database.
//
// The incumbent is replayed as just another candidate over the same rows rather
// than having its historical decisions read out of the table. Those historical
// decisions were made against a different prompt build and a different config,
// so using them as the baseline would attribute every prompt change since to the
// candidate.
// trimID shortens a cuid for a terminal column without losing the tail, which
// is the part that differs between adjacent requests.
func trimID(id string) string {
	if len(id) <= 20 {
		return id
	}
	return id[:8] + "…" + id[len(id)-8:]
}

func runKeeperEval(ctx context.Context, db *sql.DB, opts keeperEvalOptions, out, progress io.Writer) error {
	if len(opts.Candidates) == 0 {
		return fmt.Errorf("nothing to evaluate: pass at least one --candidate")
	}
	if opts.Passes < 1 {
		opts.Passes = eval.DefaultPasses
	}

	corpus, err := eval.LoadCorpus(ctx, db, opts.Limit)
	if err != nil {
		return err
	}
	if len(corpus) == 0 {
		return fmt.Errorf("the keeper corpus is empty: no settled access/execute decisions with a recorded prompt. " +
			"Run some credential requests through the Keeper first")
	}
	humanRows, incumbentRows := eval.CountBySource(corpus)
	fmt.Fprintf(progress, "Corpus: %d rows (%d human-labelled, %d incumbent-labelled) × %d pass(es)\n",
		len(corpus), humanRows, incumbentRows, opts.Passes)

	replay := func(label, model string) (eval.LabeledVerdict, error) {
		fmt.Fprintf(progress, "Replaying %s (%s)…\n", label, model)
		rows, rerr := eval.ReplayCandidate(ctx, eval.Candidate{
			Label:    label,
			Provider: newKeeperEvalProvider(opts.Endpoint, model),
			Model:    model,
		}, corpus, opts.Passes)
		if rerr != nil {
			return eval.LabeledVerdict{}, fmt.Errorf("replay %s: %w", label, rerr)
		}
		if opts.Explain {
			// Per-row, recorded vs replayed. Added after three hypotheses about a
			// 37.5% disagreement died to data in a row: the model contradicting
			// itself (self-agreement measured 1.000), the tier floor (applied, the
			// number did not move), and the intent-length minimum (one row of forty
			// was under it). Two models as different as a 9B local one and a hosted
			// frontier one then produced the SAME 0.625 to three decimals, which
			// rules out the model — and none of that narrowed anything, because
			// nothing could show which rows disagreed.
			//
			// A harness that reports a gap and cannot show you the gap invites
			// exactly the guessing it exists to replace.
			fmt.Fprintf(progress, "\n  %-24s %-9s %-9s %s\n", "REQUEST", "RECORDED", "REPLAYED", "")
			for _, r := range rows {
				if len(r.Replays) == 0 {
					continue
				}
				got := r.Replays[0].Decision
				mark := "  "
				if got != r.Label {
					mark = "≠ "
				}
				fmt.Fprintf(progress, "  %s%-22s %-9s %-9s\n", mark, trimID(r.ID), r.Label, got)
			}
			fmt.Fprintln(progress)
		}
		return eval.LabeledVerdict{Label: label, Verdict: eval.Score(rows)}, nil
	}

	incumbent, err := replay(opts.Incumbent, opts.Incumbent)
	if err != nil {
		return err
	}
	candidates := make([]eval.LabeledVerdict, 0, len(opts.Candidates))
	for _, model := range opts.Candidates {
		model = strings.TrimSpace(model)
		if model == "" || model == opts.Incumbent {
			// Replaying the incumbent as its own candidate would print it twice
			// with a delta of zero and imply a second, independent measurement.
			continue
		}
		lv, rerr := replay(model, model)
		if rerr != nil {
			return rerr
		}
		candidates = append(candidates, lv)
	}

	report := eval.BuildReport(incumbent, candidates, opts.Tolerance)
	if opts.Format == "json" {
		blob, jerr := report.JSON()
		if jerr != nil {
			return jerr
		}
		fmt.Fprintln(out, string(blob))
		return nil
	}
	fmt.Fprint(out, report.Table())
	return nil
}

func init() {
	keeperEvalCmd.Flags().StringVar(&flagKeeperEvalEndpoint, "endpoint", "",
		"Model endpoint to dial from this machine (default: the configured judge endpoint)")
	keeperEvalCmd.Flags().StringVar(&flagKeeperEvalIncumbent, "incumbent", "",
		"Model to use as the baseline (default: the configured judge model)")
	keeperEvalCmd.Flags().StringArrayVar(&flagKeeperEvalCandidates, "candidate", nil,
		"Candidate model to score; repeat for each one")
	keeperEvalCmd.Flags().BoolVar(&flagKeeperEvalExplain, "explain", false,
		"print recorded vs replayed decision for every row, marking the disagreements")
	keeperEvalCmd.Flags().IntVar(&flagKeeperEvalLimit, "limit", 0,
		"Cap the corpus at N rows (human-labelled rows are kept first); 0 = all")
	keeperEvalCmd.Flags().IntVar(&flagKeeperEvalPasses, "passes", eval.DefaultPasses,
		"Replay passes per prompt; replay runs at temperature 0.1, so >1 catches non-determinism")
	keeperEvalCmd.Flags().Float64Var(&flagKeeperEvalTolerance, "tolerance", 0,
		"How much extra guard-downgrade rate a candidate may have over the incumbent and still count as viable")
	// The only local-only command under `keeper` — the rest of that tree is
	// server-side, so the flag is declared here rather than persistently on
	// keeperCmd where it would advertise itself to commands that ignore it.
	keeperEvalCmd.Flags().Bool("local", false, localOnlyFlagHelp)
	keeperCmd.AddCommand(keeperEvalCmd)
}
