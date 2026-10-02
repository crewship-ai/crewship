//go:build !clionly

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

type instanceBackupUploadReceipt struct {
	Path               string `json:"path" yaml:"path"`
	Scope              string `json:"scope" yaml:"scope"`
	SizeBytes          int64  `json:"size_bytes" yaml:"size_bytes"`
	FormatVersion      int    `json:"format_version" yaml:"format_version"`
	ProofLevel         int    `json:"proof_level" yaml:"proof_level"`
	Duplicate          bool   `json:"duplicate" yaml:"duplicate"`
	ConversionRequired bool   `json:"conversion_required" yaml:"conversion_required"`
}

var adminInstanceBackupUploadCmd = &cobra.Command{
	Use:   "upload <encrypted-archive>",
	Short: "Upload an encrypted backup archive without sending a private key",
	Long: `POST /api/v1/admin/instance/backups/bundles/upload. Streams a local encrypted
archive into the instance catalog. Only checksum proof is recorded; contents
checks and a test restore remain separate. Maximum archive size: 64 GiB.
No workspace selection or decryption identity is required.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		file, err := os.Open(args[0])
		if err != nil {
			return err
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("upload requires a regular encrypted archive file")
		}
		if info.Size() > 64<<30 {
			return fmt.Errorf("backup exceeds the 64 GiB upload limit")
		}
		request, err := client.NewRequest(cmd.Context(), http.MethodPost, instanceBackupsAPI+"/bundles/upload", file)
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/octet-stream")
		request.ContentLength = info.Size()
		// No whole-request timeout: a 64 GiB archive over a slow link takes
		// hours, and expiring would cancel the server's work. The server ends
		// an upload that stops making progress; Ctrl-C cancels it here.
		transport := *client.HTTPClient
		transport.Timeout = 0
		response, err := transport.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			var failure struct {
				Error string `json:"error" yaml:"error"`
			}
			_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&failure)
			return fmt.Errorf("backup upload: HTTP %d: %s", response.StatusCode, failure.Error)
		}
		var receipt instanceBackupUploadReceipt
		if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&receipt); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(receipt, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %s (%d bytes). Checksum verified; contents and restore not yet verified.\n", receipt.Path, receipt.SizeBytes)
			if receipt.ConversionRequired {
				fmt.Fprintln(cmd.OutOrStdout(), "This legacy archive requires conversion before recovery.")
			}
		})
	},
}

func init() { adminInstanceBackupsCmd.AddCommand(adminInstanceBackupUploadCmd) }
