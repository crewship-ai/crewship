package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"regexp"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// The engine answers with at most 4 MiB of JSON; its Markdown is smaller.
const docExportResponseBytes = 4 << 20

var docExportFilename = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}\.md$`)

// export downloads the confirmed document as one file. The global
// --format/-f still selects the receipt format; --as names the file type.
func newDocExportCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "export <page-slug>", Args: cobra.ExactArgs(1), Short: "Download the confirmed document as a Markdown file", Long: `Download the document's confirmed content as Markdown: headings, paragraphs,
lists, checklists, quotes, simple tables, links, bold, italic, strikethrough,
inline code and fenced code blocks with their language. Content Markdown cannot
represent (for example a code block inside a table) fails with an explanation
instead of being dropped. It reads what doc read reads, with the same access:
you need read access to the document.

The file is written atomically to --out (default: the server's file name, such
as launch-notes.md, in the current directory) and standard output carries a
JSON receipt (path, media_type, byte_length, sha256). --out - writes the
Markdown itself to standard output instead.`, Example: "  crewship doc export @<artifact-id>\n  crewship doc export launch-notes --as md -o notes.md", RunE: func(cmd *cobra.Command, args []string) error {
		as, _ := cmd.Flags().GetString("as")
		if as != "md" && as != "markdown" {
			return cli.WithExitCode(fmt.Errorf("--as must be md; no other document export format is available yet"), cli.ExitValidation)
		}
		formatter := resolvedFormatter(cmd)
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.WithContext(cmd.Context()).Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/export?format=markdown")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || media != "text/markdown" {
			return errors.New("document export did not return text/markdown")
		}
		_, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
		name := params["filename"]
		if err != nil || !docExportFilename.MatchString(name) {
			return errors.New("document export response does not name a Markdown file")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, docExportResponseBytes+1))
		if err != nil {
			return err
		}
		if len(raw) > docExportResponseBytes || !utf8.Valid(raw) {
			return errors.New("document export response is not bounded UTF-8 Markdown")
		}
		out, _ := cmd.Flags().GetString("out")
		if out == "-" {
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		}
		if out == "" {
			out = name
		}
		if _, statErr := os.Lstat(out); statErr == nil {
			if force, _ := cmd.Flags().GetBool("force"); !force {
				return cli.WithExitCode(fmt.Errorf("%s exists; pass --force to replace it", out), cli.ExitValidation)
			}
		}
		file, err := cli.NewAtomicFile(out)
		if err != nil {
			return err
		}
		defer file.Close()
		if _, err := file.Write(raw); err != nil {
			return err
		}
		if err := file.Commit(); err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		receipt, err := json.Marshal(struct {
			Path       string `json:"path" yaml:"path"`
			MediaType  string `json:"media_type" yaml:"media_type"`
			ByteLength int    `json:"byte_length" yaml:"byte_length"`
			SHA256     string `json:"sha256" yaml:"sha256"`
		}{out, "text/markdown", len(raw), hex.EncodeToString(sum[:])})
		if err != nil {
			return err
		}
		return pageEmitMachine(formatter, receipt, "{}")
	}}
	cmd.Flags().String("as", "md", "Export file type; md (Markdown) is available")
	cmd.Flags().StringP("out", "o", "", "Output file (default: server file name in the current directory; - writes Markdown to stdout)")
	cmd.Flags().Bool("force", false, "Replace an existing --out file")
	return cmd
}
