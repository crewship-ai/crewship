package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/pageassets"
	"github.com/spf13/cobra"
)

func newPageAssetsCommand() *cobra.Command {
	root := &cobra.Command{Use: "assets", Short: "Upload and read private staged Page PNG/JPEG assets"}
	upload := &cobra.Command{Use: "upload <slug>", Short: "Upload a bounded PNG/JPEG (staged for 24 hours)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		mutation, _ := cmd.Flags().GetString("mutation-id")
		if file == "" || mutation == "" {
			return fmt.Errorf("--file and --mutation-id are required")
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
		// Limit local input too; server remains authoritative for actual format.
		raw, err := io.ReadAll(io.LimitReader(reader, pageassets.MaxInput+1))
		if err != nil {
			return err
		}
		if len(raw) > pageassets.MaxInput {
			return fmt.Errorf("image exceeds 5 MiB")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		req, err := client.NewRequest(cmd.Context(), http.MethodPost, "/api/v1/pages/"+pagePathEscape(args[0])+"/assets?mutation_id="+url.QueryEscape(mutation), bytes.NewReader(raw))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		response, err := client.HTTPClient.Do(req)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	upload.Flags().String("file", "", "PNG/JPEG file or - for stdin")
	upload.Flags().String("mutation-id", "", "Stable upload retry identity")
	show := &cobra.Command{Use: "show <slug> <asset-id>", Short: "Read private asset metadata", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/assets/" + pagePathEscape(args[1]))
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, resp)
	}}
	download := &cobra.Command{Use: "download <slug> <asset-id>", Short: "Write canonical private PNG/JPEG bytes to stdout", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := pageClient()
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/assets/" + pagePathEscape(args[1]) + "/bytes")
		if err != nil {
			return err
		}
		if resp.StatusCode != 200 {
			return printPageArtifactResponse(cmd, resp)
		}
		defer resp.Body.Close()
		_, err = io.Copy(cmd.OutOrStdout(), io.LimitReader(resp.Body, pageassets.MaxOutput+1))
		return err
	}}
	root.AddCommand(upload, show, download)
	return root
}

func init() { pageCmd.AddCommand(newPageAssetsCommand()) }
