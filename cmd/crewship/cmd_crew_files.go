package main

// crew files: list / get / save subcommands wrapping the Shared Ship
// (`/crew/shared/`) surface. Server-side routes live in
// internal/api/proxy_files.go (CrewFiles / CrewFileDownload / CrewFileSave)
// and are mounted under /api/v1/crews/{crewId}/files…
//
// Pattern mirrors cmd_agent_files.go so the table / json / yaml output and
// the atomic-download helper feel identical regardless of scope.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var crewFilesCmd = &cobra.Command{
	Use:     "files",
	Aliases: []string{"file"},
	Short:   "Inspect or write files in a crew's shared directory",
}

var crewFilesListCmd = &cobra.Command{
	Use:   "list <crew-slug-or-id>",
	Short: "List files in the crew's /crew/shared directory",
	Long: `List the entries under the crew's shared volume (the inter-agent
"Shared Ship" namespace).

Examples:
  crewship crew files list demo-crew
  crewship crew files list demo-crew --path /shared/notes
  crewship crew files list demo-crew --format json --filter '.[] | .name'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		subdir, _ := cmd.Flags().GetString("path")
		recursive, _ := cmd.Flags().GetBool("recursive")

		path := "/api/v1/crews/" + url.PathEscape(crewID) + "/files"
		q := url.Values{}
		if subdir != "" {
			q.Set("subdir", subdir)
		}
		if recursive {
			q.Set("recursive", "true")
		}
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		var body any
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		if jq, _ := cmd.Flags().GetString("filter"); jq != "" {
			return emitJSONFiltered(cmd, body)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body)
		case "yaml":
			return f.YAML(body)
		default:
			return printFilesTable(body)
		}
	},
}

var crewFilesGetCmd = &cobra.Command{
	Use:   "get <crew-slug-or-id> <path>",
	Short: "Download a file from the crew's shared directory",
	Long: `Stream a file from the crew shared volume. Defaults to stdout when
no --out is given so the output is pipe-friendly. Use --out file (or "-")
to land bytes on disk; '-' means stdout explicitly.

Examples:
  crewship crew files get demo-crew shared/notes.md
  crewship crew files get demo-crew shared/dump.bin --out /tmp/dump.bin`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		q := url.Values{}
		q.Set("path", args[1])
		resp, err := client.Get("/api/v1/crews/" + url.PathEscape(crewID) +
			"/files/download?" + q.Encode())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		out, _ := cmd.Flags().GetString("out")
		if out == "" || out == "-" {
			_, err := io.Copy(os.Stdout, resp.Body)
			return err
		}

		// Same atomic-download dance as agent files: tempfile + rename keeps
		// a partially-streamed body from clobbering a good destination.
		af, err := cli.NewAtomicFile(out)
		if err != nil {
			return err
		}
		defer af.Close()
		n, err := io.Copy(af, resp.Body)
		if err != nil {
			return fmt.Errorf("copy: %w", err)
		}
		if err := af.Commit(); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		fmt.Fprintf(os.Stderr, "%s[saved %d bytes → %s]%s\n", cli.Dim, n, out, cli.Reset)
		return nil
	},
}

var crewFilesSaveCmd = &cobra.Command{
	Use:   "save <crew-slug-or-id> <path>",
	Short: "Write a file to the crew's shared directory",
	Long: `Upload bytes to the crew shared volume. The body comes from
--content (inline string), --file (local path), or stdin when neither flag
is given.

Examples:
  echo "hi" | crewship crew files save demo-crew shared/hi.txt
  crewship crew files save demo-crew shared/note.md --content "draft"
  crewship crew files save demo-crew shared/data.bin --file /tmp/data.bin`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		content, _ := cmd.Flags().GetString("content")
		local, _ := cmd.Flags().GetString("file")
		if cmd.Flags().Changed("content") && local != "" {
			return fmt.Errorf("--content and --file are mutually exclusive")
		}

		var body io.Reader
		var size int64
		switch {
		case local != "":
			st, err := os.Stat(local)
			if err != nil {
				return fmt.Errorf("stat %s: %w", local, err)
			}
			fh, err := os.Open(local)
			if err != nil {
				return fmt.Errorf("open %s: %w", local, err)
			}
			defer fh.Close()
			body = fh
			size = st.Size()
		case cmd.Flags().Changed("content"):
			body = bytes.NewReader([]byte(content))
			size = int64(len(content))
		default:
			// stdin: buffer up-front so we can report bytes written. The save
			// surface caps payload server-side; in-memory is fine for the
			// few-MB case the CLI is designed for.
			buf, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			body = bytes.NewReader(buf)
			size = int64(len(buf))
		}

		// client.Put doesn't exist on the shared client (yet), so we build
		// the request manually with the same auth + workspace plumbing as
		// client.Do. PutFiles uses an unwrapped http.Client to avoid the
		// JSON-encoding path on the standard client.Post/.Patch.
		if err := putBytes(cmd.Context(), client,
			"/api/v1/crews/"+url.PathEscape(crewID)+"/files/save?path="+url.QueryEscape(args[1]),
			body); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Saved %d bytes to %s in crew %q.", size, args[1], args[0]))
		return nil
	},
}

// putBytes streams raw bytes via PUT, reusing the configured client's auth
// token and workspace context. The standard cli.Client.Do path JSON-encodes
// the body, which is wrong for binary file uploads — so we issue the
// request directly while still picking up the BaseURL + workspace_id query
// parameter the server expects.
func putBytes(ctx context.Context, client *cli.Client, path string, body io.Reader) error {
	// Build through client.NewRequest so workspace injection AND the issue
	// #571 token-host guard run here too — setting the bearer by hand would
	// bypass that guard and leak the token to a mismatched server host (CLI1).
	req, err := client.NewRequest(ctx, http.MethodPut, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := client.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	return nil
}

// printFilesTable renders the crew file listing identically to the agent
// one so users see consistent columns regardless of scope. The shared
// helper extractFileList already tolerates both list and {files:[…]} body
// shapes; reuse rather than re-derive.
//
// Why not just call printFilesTable from cmd_agent_files.go? It already
// exists as an unexported function in the same package — this file lives
// in the same package, so we use it directly via the name. Documenting the
// reuse to avoid future drift.

func init() {
	crewFilesListCmd.Flags().String("path", "", "Subdirectory under /crew/shared to list")
	crewFilesListCmd.Flags().Bool("recursive", false, "Recurse into subdirectories")
	jqExprFlag(crewFilesListCmd)

	crewFilesGetCmd.Flags().String("out", "", "Output path ('-' or empty: stdout)")

	crewFilesSaveCmd.Flags().String("content", "", "Inline content string (alternative to stdin / --file)")
	crewFilesSaveCmd.Flags().String("file", "", "Local file path to upload (alternative to stdin / --content)")

	crewFilesCmd.AddCommand(crewFilesListCmd)
	crewFilesCmd.AddCommand(crewFilesGetCmd)
	crewFilesCmd.AddCommand(crewFilesSaveCmd)
	crewCmd.AddCommand(crewFilesCmd)
}
