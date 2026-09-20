package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/decisions"
)

func init() { rootCmd.AddCommand(newDecisionsCommand()) }

// A local pilot: only explicitly supplied stdin/file content reaches the model.
// Does not access the server, credentials vault, memory store, or assignments.
func newDecisionsCommand() *cobra.Command {
	var provider, model, input string
	var timeout time.Duration
	var threshold float64
	var dryRun bool
	cmd := &cobra.Command{Use: "decisions", Short: "Experimental typed decisions: evaluate, suggest triage, or rerank supplied text", Long: `Opt-in Jev pilot. Sends only the supplied input to TypeSafe or OpenRouter.
Always returns JSON. Suggestions do not execute actions or change server state.
Use TYPESAFE_API_KEY or OPENROUTER_API_KEY in the environment; never in arguments.
--dry-run prints the request without a credential or network call.`}
	cmd.PersistentFlags().StringVar(&provider, "provider", "typesafe", "typesafe or openrouter")
	cmd.PersistentFlags().StringVar(&model, "model", "", "Model override (default: provider's versioned Jev 1.13 identifier)")
	cmd.PersistentFlags().StringVar(&input, "input", "-", "Input file, or - for stdin")
	cmd.PersistentFlags().DurationVar(&timeout, "timeout", 5*time.Second, "Per-call time budget (maximum 1m)")
	cmd.PersistentFlags().Float64Var(&threshold, "threshold", 0.9, "Experimental triage confidence threshold, 0.5 to 1")
	cmd.PersistentFlags().BoolVar(&dryRun, "dry-run", false, "Print the request without calling an API")
	for _, mode := range []string{"evaluate", "triage", "rerank"} {
		sub := &cobra.Command{Use: mode, Args: cobra.NoArgs}
		switch mode {
		case "evaluate":
			sub.Short = "Evaluate a JSON request containing state and questions"
		case "triage":
			sub.Short = "Suggest a Crewship subsystem and review signals from plain text"
		case "rerank":
			sub.Short = "Rank JSON {query, candidates:[{id,text}]} without dropping candidates"
		}
		sub.RunE = func(cmd *cobra.Command, _ []string) error {
			if provider != "typesafe" && provider != "openrouter" {
				return errors.New("provider must be typesafe or openrouter")
			}
			if timeout <= 0 || timeout > time.Minute {
				return errors.New("timeout must be between 0 and 1 minute")
			}
			if !(threshold >= 0.5 && threshold <= 1) {
				return errors.New("threshold must be between 0.5 and 1")
			}
			reader := cmd.InOrStdin()
			if input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return fmt.Errorf("open decision input: %w", err)
				}
				defer f.Close()
				reader = f
			}
			b, err := io.ReadAll(io.LimitReader(reader, decisions.MaxRequestBytes+1))
			if err != nil {
				return errors.New("read decision input")
			}
			if len(b) > decisions.MaxRequestBytes {
				return errors.New("decision input exceeds byte limit")
			}
			var req decisions.Request
			var rankInput decisions.RerankInput
			switch mode {
			case "evaluate":
				if err = json.Unmarshal(b, &req); err != nil {
					return errors.New("invalid decision request JSON")
				}
			case "triage":
				if strings.TrimSpace(string(b)) == "" {
					return errors.New("triage text is empty")
				}
				req = decisions.TriageRequest(string(b))
			case "rerank":
				if err = json.Unmarshal(b, &rankInput); err != nil {
					return errors.New("invalid rerank input JSON")
				}
				req, err = decisions.RerankRequest(rankInput)
				if err != nil {
					return err
				}
			}
			if model != "" {
				req.Model = model
			}
			if req.Model == "" {
				req.Model = "jev-1.13.0"
				if provider == "openrouter" {
					req.Model = "typesafe/jev-1.13"
				}
			}
			if err = req.Validate(); err != nil {
				return err
			}
			encoded, err := json.Marshal(req)
			if err != nil {
				return errors.New("encode decision input")
			}
			if len(encoded) > decisions.MaxRequestBytes {
				return errors.New("decision request exceeds byte limit")
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if dryRun {
				return enc.Encode(req)
			}
			keyName := "TYPESAFE_API_KEY"
			if provider == "openrouter" {
				keyName = "OPENROUTER_API_KEY"
			}
			key := os.Getenv(keyName)
			if strings.TrimSpace(key) == "" {
				return fmt.Errorf("%s is required (or use --dry-run)", keyName)
			}
			client, err := decisions.NewClient(provider, key, timeout)
			if err != nil {
				return err
			}
			started := time.Now()
			resp, err := client.Evaluate(cmd.Context(), req)
			if err != nil {
				return err
			}
			var result any = resp
			switch mode {
			case "triage":
				result, err = decisions.SuggestTriage(req, resp, threshold)
			case "rerank":
				ranked, rankErr := decisions.Rank(rankInput, resp)
				err = rankErr
				result = struct {
					Ranking  []decisions.RankedCandidate `json:"ranking" yaml:"ranking"`
					Response decisions.Response          `json:"response" yaml:"response"`
				}{ranked, resp}
			}
			if err != nil {
				return err
			}
			return enc.Encode(struct {
				Mode      string  `json:"mode" yaml:"mode"`
				LatencyMS float64 `json:"latency_ms" yaml:"latency_ms"`
				Result    any     `json:"result" yaml:"result"`
			}{mode, float64(time.Since(started).Microseconds()) / 1000, result})
		}
		cmd.AddCommand(sub)
	}
	return cmd
}
