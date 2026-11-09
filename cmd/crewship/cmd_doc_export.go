package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"regexp"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// docExportFormat is one file type the server really produces.
type docExportFormat struct {
	query, media string
	limit        int
	filename     *regexp.Regexp
	valid        func([]byte) bool
}

// The engine answers with at most 4 MiB of JSON: Markdown is smaller, and DOCX
// (base64 in that reply) is capped at 3 MiB.
var docExportFormats = map[string]docExportFormat{
	"md": {"markdown", "text/markdown", 4 << 20, regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}\.md$`), utf8.Valid},
	"docx": {"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", 3 << 20, regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}\.docx$`),
		func(raw []byte) bool { return bytes.HasPrefix(raw, []byte("PK\x03\x04")) }},
}

// export downloads the confirmed document as one file. The global
// --format/-f still selects the receipt format; --as names the file type.
func newDocExportCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "export <page-slug>", Args: cobra.ExactArgs(1), Short: "Download the confirmed document as a Markdown or Word file", Long: `Download the document's confirmed content as Markdown (--as md, the default)
or as a Word document (--as docx): headings, paragraphs, lists, checklists,
quotes, simple tables, links, bold, italic, strikethrough, inline code and code
blocks (Markdown keeps the language as a fence; Word keeps the text in a code
style). Math is exported as its LaTeX source. Content a format cannot represent
(for example a code block inside a table in Markdown, or lists nested deeper
than nine levels in Word) fails with an explanation instead of being dropped.
It reads what doc read reads, with the same access: you need read access to
the document.

The file is written atomically to --out (default: the server's file name, such
as launch-notes.md, in the current directory) and standard output carries a
JSON receipt (path, media_type, byte_length, sha256). --out - writes the file
itself to standard output instead.`, Example: "  crewship doc export @<artifact-id>\n  crewship doc export launch-notes --as md -o notes.md\n  crewship doc export launch-notes --as docx", RunE: func(cmd *cobra.Command, args []string) error {
		as, _ := cmd.Flags().GetString("as")
		if as == "markdown" {
			as = "md"
		}
		format, ok := docExportFormats[as]
		if !ok {
			return cli.WithExitCode(fmt.Errorf("--as must be md or docx"), cli.ExitValidation)
		}
		formatter := resolvedFormatter(cmd)
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.WithContext(cmd.Context()).Get("/api/v1/pages/" + pagePathEscape(args[0]) + "/document/export?format=" + format.query)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || media != format.media {
			return fmt.Errorf("document export did not return %s", format.media)
		}
		_, params, err := mime.ParseMediaType(response.Header.Get("Content-Disposition"))
		name := params["filename"]
		if err != nil || !format.filename.MatchString(name) {
			return fmt.Errorf("document export response does not name a .%s file", as)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, int64(format.limit)+1))
		if err != nil {
			return err
		}
		if len(raw) > format.limit || !format.valid(raw) {
			return fmt.Errorf("document export response is not a bounded .%s file", as)
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
		}{out, format.media, len(raw), hex.EncodeToString(sum[:])})
		if err != nil {
			return err
		}
		return pageEmitMachine(formatter, receipt, "{}")
	}}
	cmd.Flags().String("as", "md", "Export file type: md (Markdown) or docx (Word)")
	cmd.Flags().StringP("out", "o", "", "Output file (default: server file name in the current directory; - writes the file to stdout)")
	cmd.Flags().Bool("force", false, "Replace an existing --out file")
	return cmd
}
