package main

// POST /api/v1/pages/{slug}/board/files page board upload
// GET /api/v1/pages/{slug}/board/files/{fileId} page board download

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func newPageBoardUploadCommand() *cobra.Command {
	command := &cobra.Command{Use: "upload <slug>", Short: "Upload a bounded PNG/JPEG image into a Board", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		file, _ := cmd.Flags().GetString("file")
		mutation, _ := cmd.Flags().GetString("mutation-id")
		base, _ := cmd.Flags().GetInt64("base-seq")
		if file == "" || strings.TrimSpace(mutation) == "" || !cmd.Flags().Changed("base-seq") || base < 0 {
			return fmt.Errorf("--file, --mutation-id and nonnegative --base-seq are required")
		}
		var reader io.Reader = cmd.InOrStdin()
		if file != "-" {
			source, err := os.Open(file)
			if err != nil {
				return err
			}
			defer source.Close()
			reader = source
		}
		content, err := io.ReadAll(io.LimitReader(reader, (5<<20)+1))
		if err != nil {
			return err
		}
		if len(content) == 0 || len(content) > 5<<20 {
			return fmt.Errorf("image must be nonempty and at most 5 MiB")
		}
		mime := http.DetectContentType(content)
		if mime != "image/png" && mime != "image/jpeg" {
			return fmt.Errorf("only PNG/JPEG images are supported")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		query := url.Values{"base_seq": {strconv.FormatInt(base, 10)}, "mutation_id": {mutation}}
		request, err := client.NewRequest(cmd.Context(), http.MethodPost, "/api/v1/pages/"+pagePathEscape(args[0])+"/board/files?"+query.Encode(), bytes.NewReader(content))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", mime)
		response, err := client.HTTPClient.Do(request)
		if err != nil {
			return err
		}
		return printPageArtifactResponse(cmd, response)
	}}
	command.Flags().String("file", "", "PNG/JPEG file; - reads stdin")
	command.Flags().String("mutation-id", "", "Required stable identity; repeat it only for the identical upload")
	command.Flags().Int64("base-seq", -1, "Required current Board sequence")
	return command
}
func newPageBoardDownloadCommand() *cobra.Command {
	command := &cobra.Command{Use: "download <slug> <file-id>", Short: "Download an authorized Board image into a new local file", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		output, _ := cmd.Flags().GetString("output")
		if output == "" || output == "-" {
			return fmt.Errorf("--output must name a new local file")
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/board/files/" + pagePathEscape(args[1]))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(response.Body, (8<<20)+1))
		if err != nil {
			return err
		}
		if len(content) == 0 || len(content) > 8<<20 {
			return fmt.Errorf("invalid Board image size")
		}
		mime := http.DetectContentType(content)
		if mime != "image/png" && mime != "image/jpeg" {
			return fmt.Errorf("response is not a PNG/JPEG image")
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
		return resolvedFormatter(cmd).AutoHuman(map[string]any{"file_id": args[1], "bytes": len(content), "output": output}, func() { fmt.Fprintf(cmd.OutOrStdout(), "Saved %d bytes\n", len(content)) })
	}}
	command.Flags().String("output", "", "Required new destination file; existing files are never overwritten")
	return command
}
