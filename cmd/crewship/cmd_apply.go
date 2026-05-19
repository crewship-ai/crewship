package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/manifest"
)

// applyCmd is the kubectl-style entry point for declarative workspace
// management. A manifest is a YAML (or JSON) file that describes a
// crew or a whole workspace as data: agents, skills, credentials,
// MCP servers, devcontainer. Apply converges the target workspace
// toward that state through normal REST calls — no direct DB writes,
// so RBAC and audit logging behave identically to UI clicks.
//
// Examples:
//
//	crewship apply --file crew.yaml
//	crewship apply --file team.workspace.yaml --strict
//	crewship apply --file crew.yaml --from-env
//	crewship apply --file crew.yaml --secrets-file secrets.env
//	crewship apply --file crew.yaml --dry-run
//	crewship apply --file crew.yaml --replace --yes
var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Apply a workspace manifest (crew, agents, skills, credentials)",
	Long: `Apply a YAML manifest that describes a crew or workspace. Re-running
apply is idempotent: missing resources are created, existing ones are
updated to match the manifest, AND resources that disappeared from the
manifest are deleted (manifest = source of truth, like Terraform). Any
destructive operation prompts for confirmation unless --yes is set.

Conflict modes (mutually exclusive):
  default        Sync: create / update / delete to match manifest
  --strict       Fail if any resource in the manifest already exists
  --replace      Delete every matching resource before recreating it

Credential values are NEVER stored in the manifest itself. By default
declared credentials are created as PENDING slots that show up in the
UI as "Needs value". Pass --from-env to read values from environment
variables named after the credential's env: field, or --secrets-file
to load them from a KEY=VALUE file.`,
	RunE: runApply,
}

func init() {
	// `-f` is the global shorthand for --format; manifest's "file"
	// flag therefore goes long-form only. The string still mirrors
	// kubectl's `kubectl apply -f` enough that users find it by
	// reading the help.
	applyCmd.Flags().String("file", "", "Path to manifest YAML/JSON file (use - for stdin)")
	applyCmd.Flags().Bool("dry-run", false, "Validate and show the plan without making changes")
	applyCmd.Flags().Bool("strict", false, "Fail if any resource already exists")
	applyCmd.Flags().Bool("replace", false, "Delete and recreate existing resources (destructive)")
	applyCmd.Flags().Bool("from-env", false, "Read credential values from process environment")
	applyCmd.Flags().String("secrets-file", "", "Load credential values from a KEY=VALUE file")
	applyCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompts (required for destructive plans in non-TTY)")
	_ = applyCmd.MarkFlagRequired("file")

	rootCmd.AddCommand(applyCmd)
}

func runApply(cmd *cobra.Command, args []string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}

	path, _ := cmd.Flags().GetString("file")
	if path == "" {
		return fmt.Errorf("--file is required (path to manifest YAML or '-' for stdin)")
	}
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	strict, _ := cmd.Flags().GetBool("strict")
	replace, _ := cmd.Flags().GetBool("replace")
	fromEnv, _ := cmd.Flags().GetBool("from-env")
	secretsFile, _ := cmd.Flags().GetString("secrets-file")
	yes, _ := cmd.Flags().GetBool("yes")

	if strict && replace {
		return fmt.Errorf("--strict and --replace are mutually exclusive")
	}

	bundle, err := loadManifestBundle(path)
	if err != nil {
		return err
	}
	if err := bundle.Validate(); err != nil {
		return err
	}

	mode := manifest.ApplyUpsert
	switch {
	case strict:
		mode = manifest.ApplyStrict
	case replace:
		mode = manifest.ApplyReplace
	}

	secrets, err := buildSecretsSource(fromEnv, secretsFile)
	if err != nil {
		return err
	}

	client := manifest.NewClient(newAPIClient())

	// Two-pass run: build plan, render it, prompt on destructive
	// operations, then execute. The plan-then-confirm shape mirrors
	// `terraform plan && terraform apply` so users always know what
	// they're about to mutate.
	plan, err := manifest.BuildPlan(cmd.Context(), client, bundle, manifest.Options{
		Mode:    mode,
		Secrets: secrets,
	})
	if err != nil {
		return err
	}

	printPlan(plan, dryRun)

	if dryRun {
		printSummary(plan, nil)
		return nil
	}

	if plan.HasDestructive() && !yes {
		_, _, _, deletes := plan.Summary()
		question := fmt.Sprintf("Plan includes %d destructive operation%s. Continue?", deletes, plural(deletes))
		if !confirmInteractive(question) {
			return fmt.Errorf("aborted")
		}
		yes = true
	}

	result, err := manifest.Apply(cmd.Context(), client, bundle, manifest.Options{
		Mode:     mode,
		Secrets:  secrets,
		Yes:      yes,
		OnReport: func(string) { /* plan already printed */ },
	})
	// Any error from Apply means nothing was committed past the
	// point of failure — print whatever summary we have and bail.
	// ErrConfirmationRequired specifically maps to a user-facing
	// "aborted" so the operator sees what to do (pass --yes or
	// remove the destructive operation from the manifest) rather
	// than a confusing wrapped-error message. Treating it as a
	// successful exit (the previous behaviour) would let the
	// remainder of this function deref a possibly-nil result and
	// fool downstream tooling into thinking apply succeeded.
	if err != nil {
		printSummary(plan, result)
		if errors.Is(err, manifest.ErrConfirmationRequired) {
			return fmt.Errorf("aborted: destructive plan requires confirmation (pass --yes)")
		}
		return err
	}

	printSummary(plan, result)

	if result != nil && len(result.PendingCredentials) > 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintf(os.Stdout, "%sPENDING credentials (set values in the UI, or via 'crewship credential set'):%s\n",
			cli.Yellow, cli.Reset)
		seen := map[string]bool{}
		for _, env := range result.PendingCredentials {
			if seen[env] {
				continue
			}
			seen[env] = true
			fmt.Fprintf(os.Stdout, "  - %s\n", env)
		}
	}

	provisionHintForCrews(bundle)
	return nil
}

// loadManifestBundle reads from a file path, or from stdin when the
// path is "-". The "-" sentinel matches POSIX convention used by
// every tool that reads optional file input (curl, jq, kubectl).
//
// Stdin is hard-capped at the same 4 MiB ceiling LoadFile enforces.
// io.LimitReader silently truncates on overflow, so reading +1 byte
// past the limit + comparing length is the only way to distinguish
// "exactly 4 MiB" from "more than 4 MiB"; without that distinction,
// a 10 MiB pipe would parse-and-apply the first 4 MiB and skip the
// rest, producing partially-applied sync state — unsafe given
// destructive defaults.
func loadManifestBundle(path string) (*manifest.Bundle, error) {
	if path != "-" {
		return manifest.LoadFile(path)
	}
	const maxStdinManifestBytes = 4 << 20
	data, err := io.ReadAll(io.LimitReader(os.Stdin, maxStdinManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if len(data) > maxStdinManifestBytes {
		return nil, fmt.Errorf("manifest from stdin exceeds %d bytes; split the file or write it to disk first", maxStdinManifestBytes)
	}
	return manifest.Load(data)
}

func printPlan(plan *manifest.Plan, dryRun bool) {
	if plan == nil {
		return
	}
	if len(plan.Items) == 0 {
		fmt.Println("(no resources)")
		return
	}
	header := "Plan:"
	if dryRun {
		header = "Plan (dry-run, nothing will change):"
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, header)
	fmt.Fprint(os.Stdout, plan.Render())
}

func printSummary(plan *manifest.Plan, result *manifest.Result) {
	if plan == nil {
		return
	}
	c, u, n, d := plan.Summary()
	fmt.Fprintln(os.Stdout)
	if result == nil {
		fmt.Fprintf(os.Stdout, "Plan: %d to create, %d to update, %d unchanged, %d to delete.\n", c, u, n, d)
		return
	}
	fmt.Fprintf(os.Stdout, "Applied: %d created, %d updated, %d unchanged, %d deleted.\n",
		result.Created, result.Updated, result.Unchanged, result.Deleted)
}

// provisionHintForCrews prints a note for every crew with a
// devcontainer block, suggesting the chained provision command. The
// original `--provision` flag promised to do this automatically; that
// implementation was incomplete (no slug-to-id resolution, no progress
// streaming after the apply CLI had already exited), so we surface a
// hint instead. Operators who want to chain it run:
//
//	crewship apply --file x.yaml && crewship crew provision <slug>
func provisionHintForCrews(b *manifest.Bundle) {
	var slugs []string
	for i := range b.Documents {
		doc := &b.Documents[i]
		if doc.Spec != nil && doc.Spec.Devcontainer != nil {
			slugs = append(slugs, doc.Metadata.Slug)
		}
	}
	for i := range b.Workspaces {
		for j := range b.Workspaces[i].Spec.Crews {
			crew := &b.Workspaces[i].Spec.Crews[j]
			if crew.Devcontainer != nil {
				slugs = append(slugs, crew.EffectiveSlug(b.Workspaces[i].Metadata))
			}
		}
	}
	if len(slugs) == 0 {
		return
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stdout, "Next: build crew containers with:\n")
	for _, s := range slugs {
		fmt.Fprintf(os.Stdout, "  crewship crew provision %s\n", s)
	}
}

func buildSecretsSource(fromEnv bool, secretsFile string) (manifest.CredentialSource, error) {
	var chain manifest.ChainSecretsSource
	if secretsFile != "" {
		m, err := loadSecretsFile(secretsFile)
		if err != nil {
			return nil, err
		}
		chain = append(chain, m)
	}
	if fromEnv {
		chain = append(chain, manifest.EnvSecretsSource{Lookup: os.LookupEnv})
	}
	if len(chain) == 0 {
		return manifest.NoSecretsSource{}, nil
	}
	return chain, nil
}

// loadSecretsFile parses a KEY=VALUE file. Lines starting with # are
// comments; blank lines are skipped; values may be quoted with single
// or double quotes (Compose's `.env` shape). No env-var expansion —
// the file is the source of truth, mirroring how docker-compose
// treats --env-file.
func loadSecretsFile(path string) (manifest.MapSecretsSource, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open secrets file: %w", err)
	}
	defer f.Close()

	out := manifest.MapSecretsSource{}
	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if len(val) >= 2 && (val[0] == val[len(val)-1]) && (val[0] == '"' || val[0] == '\'') {
			val = val[1 : len(val)-1]
		}
		out[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read secrets file: %w", err)
	}
	return out, nil
}

// confirmInteractive prompts on stdin. Returns true only on explicit
// "y" or "yes" (case-insensitive). Empty input or a closed stdin
// (CI / non-TTY without --yes) returns false so destructive plans
// never run without explicit acknowledgement.
func confirmInteractive(prompt string) bool {
	fmt.Fprintf(os.Stderr, "%s [y/N] ", prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		return false
	}
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}
