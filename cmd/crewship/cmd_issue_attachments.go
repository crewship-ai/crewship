package main

// CLI parity for issue attachments (see internal/api/issue_attachments.go).
// Every /api/v1 route there has a command here — that is the repo rule, and it
// is also the point: the CLI is how an agent and an operator drive Crewship, and
// "attach the log I just captured to the issue" is the shape of the request.
//
//	crewship issue attachments ENG-4
//	crewship issue attach      ENG-4 ./crash.log
//	crewship issue attachment  ENG-4 <attachment-id> [-o ./out.log]
//	crewship issue detach      ENG-4 <attachment-id>

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// attachmentItem mirrors the API's attachmentResponse. Only the fields the CLI
// renders are declared; --output json prints the decoded struct, so anything
// added server-side that matters here needs a field here too.
type attachmentItem struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	CreatedAt   string `json:"created_at"`

	UploadedByUserID  *string `json:"uploaded_by_user_id"`
	UploadedByAgentID *string `json:"uploaded_by_agent_id"`
	UploadedByName    *string `json:"uploaded_by_name"`
}

// The paths below are written out with fmt.Sprintf at each call site rather than
// built by a shared helper, for the reason cmd_issue_links.go records: the
// CLI↔route contract test renders a call's path argument statically and drops
// anything that does not resolve to a literal beginning "/api/", so a helper
// would make these commands pass the check without ever proving the routes
// exist. The mild repetition buys real static verification.

// attachmentUploadLimit mirrors the server's cap so an oversized file is
// refused locally, naming the size, instead of after a full upload.
//
// var (not const) so the tests that exercise the BOUNDED READ can shrink it —
// same reason episodicIndexerPoll is a var. Proving "a file that grows past the
// limit between the stat and the read is refused" against 25 MiB costs a
// multi-hundred-megabyte round trip per case and proves nothing the same test at
// 64 bytes does not. Production never assigns it.
var attachmentUploadLimit int64 = 25 << 20

var issueAttachmentsCmd = &cobra.Command{
	Use:     "attachments <identifier>",
	Aliases: []string{"files"},
	Short:   "List the files attached to an issue",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/attachments",
			crewID, url.PathEscape(identifier)))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var items []attachmentItem
		if err := cli.ReadJSON(resp, &items); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "FILENAME", "TYPE", "SIZE", "BY", "ADDED"}
		rows := make([][]string, 0, len(items))
		for _, a := range items {
			by := derefStr(a.UploadedByName, "—")
			if a.UploadedByAgentID != nil && *a.UploadedByAgentID != "" {
				by += " (agent)"
			}
			rows = append(rows, []string{
				truncateID(a.ID, 12),
				// A filename is chosen by whoever uploaded the file. Strip
				// control bytes before printing so it cannot repaint the
				// terminal — same treatment a pull-request title gets.
				truncateStr(sanitizeTerminal(a.Filename), 36),
				truncateStr(a.ContentType, 24),
				binaryBytes(a.SizeBytes),
				truncateStr(sanitizeTerminal(by), 20),
				a.CreatedAt,
			})
		}
		return f.Auto(items, headers, rows)
	},
}

var issueAttachCmd = &cobra.Command{
	Use:   "attach <identifier> <file>",
	Short: "Attach a file to an issue",
	Long: `Attach a local file to an issue.

The file's type is decided by its EXTENSION against an allowlist (.txt .log .md
.csv .json .yaml .toml .xml .diff .patch .png .jpg .gif .webp .avif .pdf .zip
.gz), never by what the client claims — a server that trusted a client-supplied
content type would happily store and later serve markup from its own origin.
Anything else is refused, and the refusal lists what is allowed.

Attaching the same bytes twice is the same attachment, not a second one, so a
retry is safe. Maximum 25 MB.

Examples:
  crewship issue attach ENG-4 ./crash.log
  crewship issue attach ENG-4 ~/Desktop/screenshot.png
`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPath := args[1]
		info, err := os.Stat(localPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", localPath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s is a directory", localPath)
		}
		// The stat is an EARLY refusal, not the enforcement — see the bounded
		// copy below. It exists so an obviously oversized file is rejected
		// before the command resolves the issue over the network.
		if info.Size() > attachmentUploadLimit {
			return fmt.Errorf("%s is %s — the limit is %s",
				localPath, binaryBytes(info.Size()), binaryBytes(attachmentUploadLimit))
		}

		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}

		fh, err := os.Open(localPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", localPath, err)
		}
		defer fh.Close()

		// 25 MB server cap → assemble in memory; streaming buys nothing at this
		// size and costs a chunked body the handler would have to buffer anyway.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile("file", filepath.Base(localPath))
		if err != nil {
			return fmt.Errorf("multipart form: %w", err)
		}
		// Enforce the ceiling WHILE READING, not from the stat above.
		//
		// The stat and this read are separated by an open and a round trip to
		// the server, and the path is not held still across them: a log file
		// being appended to, a file replaced by a build, or a path an attacker
		// controls can all be small at the stat and enormous here. Trusting the
		// stat means the command buffers however much it is handed. The limit is
		// read as limit+1 so "exactly at the limit" still succeeds and one byte
		// past it is detectable rather than silently truncated into a corrupt
		// upload the server would accept.
		n, err := io.Copy(fw, io.LimitReader(fh, attachmentUploadLimit+1))
		if err != nil {
			return fmt.Errorf("multipart copy: %w", err)
		}
		if n > attachmentUploadLimit {
			return fmt.Errorf("%s grew while it was being read and is now over the %s limit",
				localPath, binaryBytes(attachmentUploadLimit))
		}
		if err := mw.Close(); err != nil {
			return fmt.Errorf("multipart close: %w", err)
		}

		resp, err := postMultipart(cmd.Context(), client,
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/attachments", crewID, url.PathEscape(identifier)),
			mw.FormDataContentType(), &buf)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var att attachmentItem
		if err := cli.ReadJSON(resp, &att); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Attached %s (%s) to %s — id %s.",
			sanitizeTerminal(att.Filename), binaryBytes(att.SizeBytes), identifier, att.ID))
		return nil
	},
}

// attachmentOutPath is the -o/--output destination for `issue attachment`.
var attachmentOutPath string

var issueAttachmentCmd = &cobra.Command{
	Use:     "attachment <identifier> <attachment-id>",
	Aliases: []string{"download"},
	Short:   "Download one file attached to an issue",
	Long: `Download an attachment.

With no -o the bytes go to stdout, so the command composes:

  crewship issue attachment ENG-4 <id> | less

With -o they are written to that path. The server's own filename is NOT used to
choose a destination: it is a value someone else typed, and letting it name a
local path is how a download writes outside the directory you ran it in.
`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get(fmt.Sprintf("/api/v1/crews/%s/issues/%s/attachments/%s",
			crewID, url.PathEscape(identifier), url.PathEscape(args[1])))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// Bound the read. The response is a file from a server this CLI may not
		// own, and "the server said it was small" is not a reason to buffer
		// whatever arrives.
		body := io.LimitReader(resp.Body, attachmentUploadLimit+1)
		if attachmentOutPath == "" {
			_, err := io.Copy(cmd.OutOrStdout(), body)
			return err
		}
		out, err := os.OpenFile(attachmentOutPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			return fmt.Errorf("create %s: %w", attachmentOutPath, err)
		}
		// A WRITABLE handle's Close is not a formality — a short write and a
		// full disk are both reported there and nowhere else, so `defer
		// out.Close()` turns "the download failed" into "the download
		// succeeded and the file is truncated". Closed explicitly, with the
		// error returned when nothing else is being returned, and the partial
		// file removed: this is the same shape `crewship backup download` uses
		// (cmd_backup_admin.go), for the same reason — a half-written file that
		// looks complete is worse than no file.
		n, err := io.Copy(out, body)
		if err != nil {
			_ = out.Close()
			_ = os.Remove(attachmentOutPath)
			return fmt.Errorf("write %s: %w", attachmentOutPath, err)
		}
		// The bound is enforced, not merely applied: LimitReader stops at
		// limit+1, so a body over the ceiling would otherwise be written out
		// one byte short of itself and reported as a successful download.
		if n > attachmentUploadLimit {
			_ = out.Close()
			_ = os.Remove(attachmentOutPath)
			return fmt.Errorf("%s is over the %s attachment limit — refusing to write a truncated file",
				args[1], binaryBytes(attachmentUploadLimit))
		}
		if cerr := out.Close(); cerr != nil {
			_ = os.Remove(attachmentOutPath)
			return fmt.Errorf("close %s: %w", attachmentOutPath, cerr)
		}
		cli.PrintSuccess(fmt.Sprintf("Wrote %s to %s.", binaryBytes(n), attachmentOutPath))
		return nil
	},
}

var issueDetachCmd = &cobra.Command{
	Use:     "detach <identifier> <attachment-id>",
	Aliases: []string{"unattach"},
	Short:   "Remove a file from an issue",
	Long: `Remove an attachment from an issue.

The stored bytes are deleted too — but only if no other issue, comment or chat
in this workspace still references the same file. Attachments are stored by
content, so two issues carrying an identical file share one copy, and removing
it from one must not take it away from the other.
`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, crewID, identifier, err := resolveIssueForLinks(args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete(fmt.Sprintf("/api/v1/crews/%s/issues/%s/attachments/%s",
			crewID, url.PathEscape(identifier), url.PathEscape(args[1])))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Attachment removed.")
		return nil
	},
}

// binaryBytes renders a byte count in BASE 1024.
//
// It is deliberately not the existing humanBytes (cmd_admin_health.go), which is
// base 1000. The numbers this one describes are the attachment caps — 25 MiB and
// 6 MiB, both powers of two — and rendering 25 MiB as "26.2 MB" would make the
// error message disagree with the limit the docs and the server both state.
func binaryBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp]), ".0")
}

func init() {
	issueAttachmentCmd.Flags().StringVarP(&attachmentOutPath, "output-file", "o", "",
		"write the file here instead of stdout")
	issueCmd.AddCommand(issueAttachmentsCmd)
	issueCmd.AddCommand(issueAttachCmd)
	issueCmd.AddCommand(issueAttachmentCmd)
	issueCmd.AddCommand(issueDetachCmd)
}
