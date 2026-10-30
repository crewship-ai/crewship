package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// Key material travels in a bounded file or stdin, never in command arguments.
// Recovery is a human operation; an agent bearer cannot fall back to a profile.
func newPageDraftKeyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "draft-key <@page-id>", Short: "Protect or recover a local draft key using current human Page read", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
			return fmt.Errorf("draft keys require human authentication")
		}
		if !strings.HasPrefix(args[0], "@") || len(args[0]) < 2 {
			return fmt.Errorf("an immutable @page-id is required")
		}
		file, _ := cmd.Flags().GetString("file")
		if file == "" {
			return fmt.Errorf("--file is required; use - for stdin")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		raw, err := io.ReadAll(io.LimitReader(reader, 4097))
		if err != nil {
			return err
		}
		if len(raw) > 4096 {
			return fmt.Errorf("draft key request exceeds 4 KiB")
		}
		if err = validateArtifactManifestJSON(raw); err != nil {
			return fmt.Errorf("invalid draft key request")
		}
		var body map[string]string
		if json.Unmarshal(raw, &body) != nil || body["human"] == "" {
			return fmt.Errorf("draft key request requires the expected human identity")
		}
		for field := range body {
			if field != "human" && field != "operation" && field != "key" && field != "envelope" {
				return fmt.Errorf("unknown draft key field %q", field)
			}
		}
		switch body["operation"] {
		case "wrap":
			key, err := base64.StdEncoding.DecodeString(body["key"])
			if err != nil || len(key) != 32 || base64.StdEncoding.EncodeToString(key) != body["key"] || body["envelope"] != "" {
				return fmt.Errorf("wrap requires a canonical base64 32-byte key")
			}
		case "unwrap":
			if body["key"] != "" || len(body["envelope"]) == 0 || len(body["envelope"]) > 2048 {
				return fmt.Errorf("unwrap requires a bounded envelope")
			}
		default:
			return fmt.Errorf("operation must be wrap or unwrap")
		}
		return artifactCLIRequest(cmd, "POST", "/api/v1/pages/"+pagePathEscape(args[0])+"/draft-key", body)
	}}
	cmd.Flags().String("file", "", "JSON {human,operation,key|envelope}; - reads stdin. Output contains sensitive key material.")
	return cmd
}
func init() { pageCmd.AddCommand(newPageDraftKeyCommand()) }
