package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

var crewLockApplyCmd = &cobra.Command{
	Use:   "lock-apply <mise-config-file> <resolution-file>",
	Short: "Apply a reviewed lock plan to unchanged local inputs and print updated JSON",
	Long: `Check a locally reviewed lock-resolve result against the current selectors,
previous lock and complete proposed bundle. Print updated mise JSON on stdout,
preserving current environment and policy fields. This command needs neither
Docker nor a Crewship server and does not write files, install or deploy tools.
Use a separate output file: shell redirection to either input would truncate it.
Hashes detect stale inputs and corruption, not a maliciously authored plan.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		raw, err := readLockApplyFile(args[0])
		if err != nil {
			return err
		}
		proposal, err := readLockApplyFile(args[1])
		if err != nil {
			return err
		}
		var plan devcontainer.MiseResolution
		dec := json.NewDecoder(bytes.NewReader(proposal))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&plan); err != nil {
			return fmt.Errorf("mise apply: invalid resolution JSON: %w", err)
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			return errors.New("mise apply: resolution must contain one JSON object")
		}
		result, err := devcontainer.ApplyMiseResolutionInput(string(raw), &plan)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(result))
		return err
	},
}

func readLockApplyFile(name string) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	const limit = 4 << 20
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errors.New("mise apply: input exceeds 4 MiB")
	}
	return raw, nil
}

func init() { crewProvisionCmd.AddCommand(crewLockApplyCmd) }
