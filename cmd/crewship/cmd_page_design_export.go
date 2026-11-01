package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"regexp"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// One byte above the server's 8 MiB export limit is already a broken response.
const designExportResponseBytes = 8 << 20

var designExportFilename = regexp.MustCompile(`^attachment; filename="([a-z0-9][a-z0-9_-]{0,63}-design-v[1-9][0-9]{0,15}\.html)"$`)

// export downloads one immutable version as a self-contained HTML file. The
// global --format/-f still selects the receipt format; --as names the file type.
func newPageDesignExportCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "export <slug>", Args: cobra.ExactArgs(1), Short: "Export an immutable Design version as one self-contained HTML file", Long: `Export every screen of one retained Design version as a single HTML file
with inline styles, embedded data: images and no scripts or network access.
Prototype links become in-page links between screens. The live canvas is never
exported: without --version an editor gets the latest saved version and a
Read or Comment audience the shared version. Save a version first to include
recent live edits.

The file is written atomically to --out (default: the server's file name, such
as checkout-design-v3.html, in the current directory) and standard output
carries a JSON receipt (path, media_type, version, byte_length, sha256).
--out - writes the HTML itself to standard output instead.`, Example: "  crewship page design export checkout --version 3 -o checkout.html", RunE: func(cmd *cobra.Command, args []string) error {
		if os.Getenv("CREWSHIP_AGENT_TOKEN") != "" {
			return errors.New("Design export requires human authentication; live agents read Design through design_get")
		}
		as, _ := cmd.Flags().GetString("as")
		if as != "html" {
			return cli.WithExitCode(fmt.Errorf("--as must be html; no other Design export format exists"), cli.ExitValidation)
		}
		version, _ := cmd.Flags().GetInt64("version")
		if version < 0 || version > 9007199254740991 {
			return cli.WithExitCode(errors.New("--version must be a positive safe integer"), cli.ExitValidation)
		}
		path := "/api/v1/pages/" + pagePathEscape(args[0]) + "/design/export?format=html"
		if version > 0 {
			path += "&version=" + strconv.FormatInt(version, 10)
		}
		client, err := pageClient()
		if err != nil {
			return err
		}
		response, err := client.WithContext(cmd.Context()).Get(path)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err := pageCheckError(response); err != nil {
			return err
		}
		media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if err != nil || media != "text/html" {
			return errors.New("Design export did not return text/html")
		}
		exported, err := strconv.ParseInt(response.Header.Get("X-Crewship-Design-Version"), 10, 64)
		match := designExportFilename.FindStringSubmatch(response.Header.Get("Content-Disposition"))
		if err != nil || exported < 1 || version > 0 && exported != version || match == nil {
			return errors.New("Design export response does not name the requested version")
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, designExportResponseBytes+1))
		if err != nil {
			return err
		}
		if len(raw) > designExportResponseBytes || !bytes.HasPrefix(raw, []byte("<!doctype html>")) {
			return errors.New("Design export response is not a bounded HTML document")
		}
		out, _ := cmd.Flags().GetString("out")
		if out == "-" {
			_, err = cmd.OutOrStdout().Write(raw)
			return err
		}
		if out == "" {
			out = match[1]
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
			Version    int64  `json:"version" yaml:"version"`
			ByteLength int    `json:"byte_length" yaml:"byte_length"`
			SHA256     string `json:"sha256" yaml:"sha256"`
		}{out, "text/html", exported, len(raw), hex.EncodeToString(sum[:])})
		if err != nil {
			return err
		}
		return pageEmitMachine(resolvedFormatter(cmd), receipt, "{}")
	}}
	cmd.Flags().String("as", "html", "Export file type; only html is available")
	cmd.Flags().Int64("version", 0, "Retained version to export; 0 exports the latest version you may read")
	cmd.Flags().StringP("out", "o", "", "Output file (default: server file name in the current directory; - writes HTML to stdout)")
	return cmd
}
