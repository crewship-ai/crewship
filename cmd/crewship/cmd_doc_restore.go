package main

// POST /api/v1/pages/{slug}/document/restore doc restore
// Original restore wire: #3268, 22b081c8e34bc5b7509ad2b708d9386fee102c40.
import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

func newDocRestoreCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "restore <page-slug>", Short: "Restore a retained checkpoint as a new revision", Long: `Send one JSON packet (at most 4 KiB) with page_id, id, schema, epoch, ifRev and
checkpoint. Read the document first to get the current epoch and rev. Keep the
exact packet for a retry after an uncertain response; never refresh its
revision or epoch. A successful restore increments the revision.`, Example: `  crewship doc restore @<artifact-id> --file restore.json`, Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		filename, _ := cmd.Flags().GetString("file")
		if filename == "" {
			return fmt.Errorf("--file is required; preserve the original restore packet for retry")
		}
		var reader io.Reader = cmd.InOrStdin()
		if filename != "-" {
			file, err := os.Open(filename)
			if err != nil {
				return err
			}
			defer file.Close()
			reader = file
		}
		raw, err := io.ReadAll(io.LimitReader(reader, 4097))
		if err != nil {
			return err
		}
		var packet map[string]json.RawMessage
		if len(raw) > 4096 || json.Unmarshal(raw, &packet) != nil || packet == nil {
			return fmt.Errorf("bounded restore JSON object required")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Post("/api/v1/pages/"+pagePathEscape(args[0])+"/document/restore", json.RawMessage(raw))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	cmd.Flags().String("file", "", "Original page_id/id/schema/epoch/ifRev/checkpoint JSON packet; - reads stdin")
	return cmd
}
