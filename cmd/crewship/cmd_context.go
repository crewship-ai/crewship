package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Context commands always use authenticated server projections, never legacy
// agent directories or local databases. Arguments are exact scoped IDs.
func newContextCommand() *cobra.Command {
	root := &cobra.Command{Use: "context", Short: "Inspect and manage your authorized restricted context"}
	chat := &cobra.Command{Use: "chat", Short: "Restricted chat context, notes, output files and attempt history"}
	for _, name := range []string{"inspect", "memory", "files", "attempts", "profile"} {
		chat.AddCommand(contextLeaf(name+" <chat-id>", name, 1))
	}
	options := contextLeaf("project-input-options <chat-id>", "project-input-options", 1)
	options.Flags().String("search", "", "Filename or project-name substring, up to 128 bytes")
	chat.AddCommand(options)
	add := contextLeaf("memory-add <chat-id> <content>", "memory-add", 2)
	chat.AddCommand(add)
	chat.AddCommand(contextLeaf("memory-delete <chat-id> <entry-id>", "memory-delete", 2))
	download := contextLeaf("download <chat-id> <file-id>", "chat-download", 2)
	download.Flags().String("out", "-", "Destination filename; - writes stdout")
	chat.AddCommand(download)
	ask := contextLeaf("ask <chat-id> <content>", "ask", 2)
	chat.AddCommand(ask)
	project := &cobra.Command{Use: "project-files", Short: "Explicit immutable project file versions"}
	project.AddCommand(contextLeaf("list <project-id>", "project-list", 1))
	upload := contextLeaf("upload <project-id> <local-file>", "project-upload", 2)
	upload.Flags().String("name", "", "Project-relative filename; defaults to local basename")
	upload.Flags().String("file-id", "", "Existing file ID for replacement; empty creates a file")
	upload.Flags().Int64("expected-revision", 0, "Exact current revision; zero only creates a new file")
	project.AddCommand(upload)
	retire := contextLeaf("retire <project-id> <file-id>", "project-retire", 2)
	retire.Flags().Int64("expected-revision", 0, "Exact current file revision (required)")
	project.AddCommand(retire)
	pd := contextLeaf("download <project-id> <version-id>", "project-download", 2)
	pd.Flags().String("out", "-", "Destination filename; - writes stdout")
	project.AddCommand(pd)
	profile := &cobra.Command{Use: "execution-profile", Short: "Inspect or explicitly set an agent's restricted backend"}
	profile.AddCommand(contextLeaf("get <agent-id>", "profile-get", 1), contextLeaf("set <agent-id> <disabled|responses_text|native_api_key>", "profile-set", 2))
	preflight := contextLeaf("issue-preflight <issue-id>", "preflight", 1)
	preflight.Flags().String("agent-id", "", "Exact assigned agent ID (required)")
	preflight.Flags().String("routine-slug", "", "Explicit declared private routine (required)")
	preflight.Flags().Bool("claim", false, "Explicitly claim this assigned issue and enqueue its private preflight")
	runs := &cobra.Command{Use: "routine-runs", Short: "Your private routine receipts and exact authorized results"}
	runs.AddCommand(contextLeaf("list", "runs-list", 0), contextLeaf("result <run-id>", "runs-result", 1))
	root.AddCommand(chat, project, profile, preflight, runs, contextLeaf("routines", "routines-list", 0), contextLeaf("pages", "pages-list", 0))
	return root
}

func contextLeaf(use, action string, count int) *cobra.Command {
	return &cobra.Command{Use: use, Short: strings.ReplaceAll(action, "-", " ") + " through the authenticated restricted API", Args: cobra.ExactArgs(count), RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		return executeContextCommand(cmd, client.WithContext(cmd.Context()), action, args)
	}}
}

func contextID(id string) (string, error) {
	if id == "" || strings.ContainsAny(id, "/\\?#") || id == "." || id == ".." {
		return "", fmt.Errorf("an exact resource ID is required")
	}
	return url.PathEscape(id), nil
}

func executeContextCommand(cmd *cobra.Command, client *cli.Client, action string, args []string) error {
	var first, second string
	var err error
	if len(args) > 0 {
		first, err = contextID(args[0])
		if err != nil {
			return err
		}
	}
	if len(args) > 1 && (action == "memory-delete" || action == "chat-download" || action == "project-retire" || action == "project-download") {
		second, err = contextID(args[1])
		if err != nil {
			return err
		}
	}
	workspace := url.PathEscape(client.GetWorkspaceID())
	method := http.MethodGet
	var endpoint string
	var body any
	download, stream := false, false
	switch action {
	case "inspect", "memory":
		endpoint = "/api/v1/chats/" + first + "/restricted-context"
		if action == "memory" {
			endpoint += "?kind=memory"
		}
	case "project-input-options":
		search, _ := cmd.Flags().GetString("search")
		search = strings.TrimSpace(search)
		if len(search) > 128 {
			return fmt.Errorf("search must contain at most 128 bytes")
		}
		endpoint = "/api/v1/chats/" + first + "/project-input-options"
		if search != "" {
			endpoint += "?search=" + url.QueryEscape(search)
		}
	case "files":
		endpoint = "/api/v1/chats/" + first + "/restricted-files"
	case "attempts":
		endpoint = "/api/v1/chats/" + first + "/restricted-attempts"
	case "profile":
		endpoint = "/api/v1/chats/" + first + "/execution-profile"
	case "memory-add":
		if strings.TrimSpace(args[1]) == "" || len(args[1]) > 8192 {
			return fmt.Errorf("memory content must contain 1–8192 bytes")
		}
		method = http.MethodPost
		endpoint = "/api/v1/chats/" + first + "/restricted-memory"
		body = map[string]any{"content": args[1]}
	case "memory-delete":
		method = http.MethodDelete
		endpoint = "/api/v1/chats/" + first + "/restricted-memory/" + second
	case "chat-download":
		endpoint = "/api/v1/chats/" + first + "/restricted-files/" + second + "/download"
		download = true
	case "ask":
		if strings.TrimSpace(args[1]) == "" || len(args[1]) > 32768 {
			return fmt.Errorf("chat content must contain 1–32768 bytes")
		}
		method = http.MethodPost
		endpoint = "/api/v1/chats/" + first + "/restricted-run"
		body = map[string]any{"content": args[1]}
		stream = true
	case "pages-list":
		endpoint = "/api/v1/workspaces/" + workspace + "/restricted-pages"
	case "routines-list":
		endpoint = "/api/v1/workspaces/" + workspace + "/restricted-routines"
	case "project-list":
		endpoint = "/api/v1/workspaces/" + workspace + "/projects/" + first + "/files"
	case "project-upload":
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil {
			return err
		}
		if !stat.Mode().IsRegular() {
			return fmt.Errorf("upload requires a regular file")
		}
		content, err := io.ReadAll(io.LimitReader(f, access.MaxProjectFileBytes+1))
		if err != nil {
			return err
		}
		if len(content) > access.MaxProjectFileBytes {
			return fmt.Errorf("project file exceeds %d bytes", access.MaxProjectFileBytes)
		}
		name, _ := cmd.Flags().GetString("name")
		if name == "" {
			name = filepath.Base(args[1])
		}
		id, _ := cmd.Flags().GetString("file-id")
		revision, _ := cmd.Flags().GetInt64("expected-revision")
		if revision < 0 || (id != "" && revision < 1) || (id == "" && revision != 0) {
			return fmt.Errorf("replacement requires --file-id and positive --expected-revision; create requires revision zero")
		}
		if id != "" {
			if _, err = contextID(id); err != nil {
				return err
			}
		}
		method = http.MethodPost
		endpoint = "/api/v1/workspaces/" + workspace + "/projects/" + first + "/files"
		body = struct {
			FileID   string `json:"file_id,omitempty" yaml:"file_id,omitempty"`
			Name     string `json:"name" yaml:"name"`
			Revision int64  `json:"expected_revision" yaml:"expected_revision"`
			Content  []byte `json:"content_base64" yaml:"content_base64"`
		}{id, name, revision, content}
	case "project-retire":
		revision, _ := cmd.Flags().GetInt64("expected-revision")
		if revision < 1 {
			return fmt.Errorf("--expected-revision must be positive")
		}
		method = http.MethodDelete
		endpoint = "/api/v1/workspaces/" + workspace + "/projects/" + first + "/files/" + second
		body = map[string]any{"expected_revision": revision}
	case "project-download":
		endpoint = "/api/v1/workspaces/" + workspace + "/projects/" + first + "/files/" + second + "/download"
		download = true
	case "profile-get", "profile-set":
		endpoint = "/api/v1/agents/" + first + "/restricted-execution"
		if action == "profile-set" {
			if args[1] != "disabled" && args[1] != "responses_text" && args[1] != "native_api_key" {
				return fmt.Errorf("unsupported execution profile")
			}
			method = http.MethodPut
			body = map[string]any{"profile": args[1]}
		}
	case "preflight":
		claim, _ := cmd.Flags().GetBool("claim")
		agent, _ := cmd.Flags().GetString("agent-id")
		routine, _ := cmd.Flags().GetString("routine-slug")
		if !claim || agent == "" || routine == "" {
			return fmt.Errorf("--claim, --agent-id and --routine-slug are required")
		}
		if _, err = contextID(agent); err != nil {
			return err
		}
		method = http.MethodPost
		endpoint = "/api/v1/workspaces/" + workspace + "/issues/" + first + "/private-preflight"
		body = map[string]any{"agent_id": agent, "routine_slug": routine}
	case "runs-list":
		endpoint = "/api/v1/workspaces/" + workspace + "/restricted-routine-runs"
	case "runs-result":
		endpoint = "/api/v1/workspaces/" + workspace + "/restricted-routine-runs/" + first
	default:
		return fmt.Errorf("unknown context command")
	}
	if stream {
		client = client.WithTimeout(30 * time.Minute)
	}
	resp, err := client.Do(method, endpoint, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err = cli.CheckError(resp); err != nil {
		return err
	}
	if download {
		return saveContextDownload(cmd, resp.Body)
	}
	if stream {
		return readContextSSE(cmd, resp.Body)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var result any
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	formatter := newFormatter()
	formatter.Writer = cmd.OutOrStdout()
	switch formatter.Format {
	case "yaml":
		return formatter.YAML(result)
	case "ndjson":
		return formatter.NDJSON(result)
	case "quiet":
		return nil
	default:
		return formatter.JSON(result)
	}
}

func saveContextDownload(cmd *cobra.Command, reader io.Reader) error {
	out, _ := cmd.Flags().GetString("out")
	if out == "-" {
		_, err := io.Copy(cmd.OutOrStdout(), reader)
		return err
	}
	if out == "" {
		return fmt.Errorf("--out is required")
	}
	file, err := cli.NewAtomicFile(out)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err = io.Copy(file, reader); err != nil {
		return err
	}
	return file.Commit()
}

func init() { rootCmd.AddCommand(newContextCommand()) }

func readContextSSE(cmd *cobra.Command, reader io.Reader) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 128*1024)
	event := ""
	done := false
	for scanner.Scan() {
		line := scanner.Text()
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), line); err != nil {
			return err
		}
		if strings.HasPrefix(line, "data:") {
			var frame struct {
				Type string `json:"type" yaml:"type"`
			}
			if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &frame) == nil && frame.Type != "" {
				event = frame.Type
			}
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if line == "" {
			if event == "error" {
				return fmt.Errorf("restricted chat failed; inspect the streamed error event")
			}
			if event == "done" {
				done = true
			}
			event = ""
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("restricted chat stream ended before completion")
	}
	return nil
}
