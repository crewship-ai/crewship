package main

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Three small subcommands grouped together because they all hit
// agent-scoped endpoints that have the exact same {agentId, path} shape:
//
//	agent files <agent>            → /api/v1/agents/{id}/files
//	agent files <agent> --download → /api/v1/agents/{id}/files/download
//	agent inbox <agent>            → /api/v1/agents/{id}/inbox
//	agent git-log <agent>          → /api/v1/agents/{id}/git-log
//
// They share the resolveAgentID + queryString pattern, so they live in
// one file rather than five.

var agentFilesCmd = &cobra.Command{
	Use:               "files <agent-slug-or-id>",
	Short:             "List or download files in an agent's working directory",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	Long: `List the agent's output directory or download a specific file.

Agents create artefacts in /output/{slug}/ inside their container — code,
docs, snapshots, etc. This is the same view the web Files panel shows.

Examples:
  crewship agent files viktor
  crewship agent files viktor --download README.md
  crewship agent files viktor --download report.txt --out /tmp/saved.txt
  crewship agent files viktor --format json --filter '.[] | .name'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		download, _ := cmd.Flags().GetString("download")
		if download != "" {
			return downloadAgentFile(client, agentID, download, cmd)
		}

		var body any
		path := "/api/v1/agents/" + url.PathEscape(agentID) + "/files"
		if err := getJSON(client, path, &body); err != nil {
			return err
		}
		jq, _ := cmd.Flags().GetString("filter")
		if jq != "" {
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

// downloadAgentFile streams the file body to disk (or stdout when --out=-).
// Streams rather than buffers because agent outputs can be large (e.g.
// build artefacts, log archives).
func downloadAgentFile(client *cli.Client, agentID, fileName string, cmd *cobra.Command) error {
	out, _ := cmd.Flags().GetString("out")
	if out == "" {
		out = filepath.Base(fileName)
	}

	q := url.Values{}
	q.Set("path", fileName)
	resp, err := client.Get("/api/v1/agents/" + url.PathEscape(agentID) + "/files/download?" + q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}

	if out == "-" {
		_, err := io.Copy(os.Stdout, resp.Body)
		return err
	}
	// Atomic download: stream into a tempfile next to the target, then
	// rename only on success. Without this, Ctrl-C or a transport hiccup
	// mid-copy clobbers an existing good file with a truncated one.
	af, err := cli.NewAtomicFile(out)
	if err != nil {
		return err
	}
	defer af.Close() // discards tempfile if Commit didn't run
	n, err := io.Copy(af, resp.Body)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}
	if err := af.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s[saved %d bytes → %s]%s\n", cli.Dim, n, out, cli.Reset)
	return nil
}

// printFilesTable handles the heterogeneous shapes the API may return
// (slice of file entries, or a wrapped {files: [...]}). Defensive parse
// so a server-side schema bump doesn't immediately break the CLI.
func printFilesTable(body any) error {
	files := extractFileList(body)
	if len(files) == 0 {
		fmt.Printf("%sNo files.%s\n", cli.Dim, cli.Reset)
		return nil
	}
	fmt.Printf("%s%-50s  %10s  %s%s\n", cli.Bold, "NAME", "SIZE", "MODIFIED", cli.Reset)
	for _, f := range files {
		name, _ := f["name"].(string)
		if name == "" {
			name, _ = f["path"].(string)
		}
		size := int64(0)
		if v, ok := f["size"].(float64); ok {
			size = int64(v)
		}
		mod := ""
		if v, ok := f["modified"].(string); ok {
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				mod = t.Format("2006-01-02 15:04")
			}
		}
		fmt.Printf("%-50s  %10d  %s\n", truncateString(name, 50), size, mod)
	}
	return nil
}

// extractFileList tolerates both `[{...}, ...]` and `{"files":[...]}` shapes.
func extractFileList(body any) []map[string]any {
	switch v := body.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for _, e := range v {
			if m, ok := e.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		if files, ok := v["files"].([]any); ok {
			out := make([]map[string]any, 0, len(files))
			for _, e := range files {
				if m, ok := e.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out
		}
	}
	return nil
}

var agentInboxCmd = &cobra.Command{
	Use:               "inbox <agent-slug-or-id>",
	Short:             "Show messages received by an agent from peers",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	Long: `List peer-to-peer messages addressed to this agent. Useful for
debugging cross-agent collaboration: did the message arrive? was it read?`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		var body any
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/inbox", &body); err != nil {
			return err
		}
		jq, _ := cmd.Flags().GetString("filter")
		if jq != "" {
			return emitJSONFiltered(cmd, body)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body)
		case "yaml":
			return f.YAML(body)
		default:
			return printInboxTable(body)
		}
	},
}

func printInboxTable(body any) error {
	msgs, _ := body.([]any)
	if msgs == nil {
		if m, ok := body.(map[string]any); ok {
			if v, ok := m["messages"].([]any); ok {
				msgs = v
			}
		}
	}
	if len(msgs) == 0 {
		fmt.Printf("%sInbox empty.%s\n", cli.Dim, cli.Reset)
		return nil
	}
	fmt.Printf("%s%-19s  %-15s  %s%s\n", cli.Bold, "WHEN", "FROM", "MESSAGE", cli.Reset)
	for _, raw := range msgs {
		m, _ := raw.(map[string]any)
		if m == nil {
			continue
		}
		ts, _ := m["created_at"].(string)
		if ts == "" {
			ts, _ = m["ts"].(string)
		}
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			ts = t.Format("2006-01-02 15:04")
		}
		from, _ := m["from"].(string)
		if from == "" {
			from, _ = m["sender"].(string)
		}
		body, _ := m["body"].(string)
		if body == "" {
			body, _ = m["content"].(string)
		}
		fmt.Printf("%-19s  %-15s  %s\n", ts, truncateString(from, 15),
			strings.ReplaceAll(truncateString(body, 60), "\n", " "))
	}
	return nil
}

var agentGitLogCmd = &cobra.Command{
	Use:               "git-log <agent-slug-or-id>",
	Short:             "Show git log inside the agent's container",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var body any
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/git-log", &body); err != nil {
			return err
		}
		// Server returns commits or text — treat both uniformly through emitJSONFiltered for json,
		// or print as the table.
		jq, _ := cmd.Flags().GetString("filter")
		if jq != "" {
			return emitJSONFiltered(cmd, body)
		}
		f := newFormatter()
		switch f.Format {
		case "json":
			return f.JSON(body)
		case "yaml":
			return f.YAML(body)
		default:
			printGitLogTable(body)
			return nil
		}
	},
}

func printGitLogTable(body any) {
	commits, _ := body.([]any)
	if commits == nil {
		if m, ok := body.(map[string]any); ok {
			if v, ok := m["commits"].([]any); ok {
				commits = v
			}
		}
	}
	if len(commits) == 0 {
		fmt.Printf("%sNo commits.%s\n", cli.Dim, cli.Reset)
		return
	}
	for _, raw := range commits {
		c, _ := raw.(map[string]any)
		if c == nil {
			continue
		}
		hash, _ := c["hash"].(string)
		if hash == "" {
			hash, _ = c["sha"].(string)
		}
		msg, _ := c["message"].(string)
		if msg == "" {
			msg, _ = c["subject"].(string)
		}
		when, _ := c["when"].(string)
		if when == "" {
			when, _ = c["date"].(string)
		}
		shortHash := hash
		if len(shortHash) > 8 {
			shortHash = shortHash[:8]
		}
		fmt.Printf("%s%s%s  %s%-16s%s  %s\n",
			cli.Yellow, shortHash, cli.Reset,
			cli.Dim, when, cli.Reset,
			truncateString(msg, 60))
	}
}

// agentFileWriteCmd uploads bytes to an agent's working directory. Mirrors
// the download path on the same file but in reverse: stdin / --from /
// --content as source, PUT to /api/v1/agents/{id}/files/save with the
// target path as a query string.
//
// The server-side handler (ProxyHandler.AgentFileSave) reads r.Body raw —
// no JSON envelope — so we issue the PUT through the byte-level helper
// rather than the JSON-encoding client.Patch / client.Post wrappers.
var agentFileWriteCmd = &cobra.Command{
	Use:   "file-write <agent-slug-or-id> <path>",
	Short: "Write a file into an agent's working directory",
	Long: `Upload bytes to the agent's /output/<slug>/ namespace. Source comes
from --from (local file), --content (inline string), or stdin when neither
flag is given.

Examples:
  echo "draft" | crewship agent file-write viktor notes/draft.md
  crewship agent file-write viktor config/x.toml --from ./local.toml
  crewship agent file-write viktor README.md --content "# hi"`,
	Args:              cobra.ExactArgs(2),
	ValidArgsFunction: completeAgentSlug,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		content, _ := cmd.Flags().GetString("content")
		from, _ := cmd.Flags().GetString("from")
		if cmd.Flags().Changed("content") && from != "" {
			return fmt.Errorf("--content and --from are mutually exclusive")
		}

		var body io.Reader
		var size int64
		switch {
		case from != "":
			st, err := os.Stat(from)
			if err != nil {
				return fmt.Errorf("stat %s: %w", from, err)
			}
			fh, err := os.Open(from)
			if err != nil {
				return fmt.Errorf("open %s: %w", from, err)
			}
			defer fh.Close()
			body = fh
			size = st.Size()
		case cmd.Flags().Changed("content"):
			body = bytes.NewReader([]byte(content))
			size = int64(len(content))
		default:
			buf, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("read stdin: %w", err)
			}
			body = bytes.NewReader(buf)
			size = int64(len(buf))
		}

		// putBytes lives in cmd_crew_files.go in the same package; it
		// handles base URL, auth, workspace_id injection without forcing
		// the body through json.Marshal (which would corrupt binaries).
		if err := putBytes(cmd.Context(), client,
			"/api/v1/agents/"+url.PathEscape(agentID)+"/files/save?path="+url.QueryEscape(args[1]),
			body); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Wrote %d bytes to %s in agent %s.", size, args[1], args[0]))
		return nil
	},
}

func init() {
	agentFilesCmd.Flags().String("download", "", "Download this specific file instead of listing")
	agentFilesCmd.Flags().String("out", "", "Output path for --download (default: basename of file, '-' for stdout)")
	jqExprFlag(agentFilesCmd)
	jqExprFlag(agentInboxCmd)
	jqExprFlag(agentGitLogCmd)

	agentFileWriteCmd.Flags().String("content", "", "Inline content string (alternative to stdin / --from)")
	agentFileWriteCmd.Flags().String("from", "", "Local file path to upload (alternative to stdin / --content)")

	agentCmd.AddCommand(agentFilesCmd)
	agentCmd.AddCommand(agentInboxCmd)
	agentCmd.AddCommand(agentGitLogCmd)
	agentCmd.AddCommand(agentFileWriteCmd)
}
