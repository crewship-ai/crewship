package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/access"
)

func addGuestPolicyFlags(cmd *cobra.Command) {
	cmd.Flags().String("access-mode", "trusted", "Access mode: trusted or restricted (Guest, account-wide)")
	cmd.Flags().String("rights-file", "", "Initial resource rights JSON array (file or - for stdin); requires restricted mode")
}

func addGuestPolicy(cmd *cobra.Command, body map[string]any) error {
	mode, _ := cmd.Flags().GetString("access-mode")
	if mode == "" {
		mode = "trusted"
	}
	file, _ := cmd.Flags().GetString("rights-file")
	if mode != "trusted" && mode != "restricted" {
		return fmt.Errorf("access-mode must be trusted or restricted")
	}
	if file != "" && mode != "restricted" {
		return fmt.Errorf("rights-file requires --access-mode restricted")
	}
	if mode == "trusted" {
		return nil
	}
	rights := []access.Right{}
	if file != "" {
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		decoder := json.NewDecoder(io.LimitReader(reader, 128*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&rights); err != nil {
			return fmt.Errorf("read initial rights: %w", err)
		}
		if rights == nil || len(rights) > 256 {
			return fmt.Errorf("rights must be an array of at most 256 grants")
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("rights file must contain one JSON array")
		}
	}
	body["access_mode"], body["rights"] = mode, rights
	return nil
}
