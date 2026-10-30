package main

// What a web-chat send can carry besides its text, for `crewship run` and
// `crewship ask`:
//
//	--attach <file>       upload into the chat, name the paths in the message
//	--page <slug>         ask the server to attach a Page snapshot (page_context)
//	--ask-form <form-id>  submit one of the agent's ask forms (ask_submission)
//	--create-document     the composer's "Create document" intent
//
// Each is the CLI half of one composer behaviour and builds the SAME bytes the
// browser sends, so the server's checks (the Page access re-check, the ask-form
// re-render, the document-creation receipt) accept or refuse a CLI send for
// exactly the reasons they would a browser one. None of them is trusted
// client-side: the server rebuilds every one of these from its own records.
//
// Kept out of cmd_run.go so the flag surface can grow without every change
// touching the run loop.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/documents"
)

// runMessageExtras is the metadata the next send carries in addition to any
// Document target. Set by prepareRunExtras, consumed by
// sendRunMessageWithExtras, cleared by the caller's defer. Package level for
// the reason the AI-first latches are (cmd_run_metadata.go).
var runMessageExtras map[string]any

// runExtrasFlags is the parsed flag set. Parsed once, validated before any
// chat is created, so a typo in a path does not leave an empty session behind.
type runExtrasFlags struct {
	attach         []string
	page           string
	askForm        string
	askVars        []string
	createDocument bool
}

func (f runExtrasFlags) any() bool {
	return len(f.attach) > 0 || f.page != "" || f.askForm != "" || f.createDocument
}

// metadataFlags reports the flags that ride on the send's metadata rather
// than in its text — the ones an interactive session cannot carry, because its
// later turns are plain sends.
func (f runExtrasFlags) metadataFlags() bool {
	return f.page != "" || f.askForm != "" || f.createDocument
}

func addRunExtrasFlags(cmd *cobra.Command, withAskForm, withCreate bool) {
	cmd.Flags().StringArray("attach", nil, "Upload a file into the chat and name it in the message (repeatable)")
	cmd.Flags().String("page", "", "Attach this Page (slug) as context; the server snapshots it if you and the agent can read it")
	if withAskForm {
		cmd.Flags().String("ask-form", "", "Submit this ask form (id) of the agent with --var answers; needs --chat")
		cmd.Flags().StringArray("var", nil, "With --ask-form: answer one field, name=value (repeat; a file/photo field takes a local path)")
	}
	if withCreate {
		cmd.Flags().Bool("create-document", false, "Ask the agent to create a new private Document from this message (needs a private chat)")
	}
}

func readRunExtrasFlags(cmd *cobra.Command) runExtrasFlags {
	var f runExtrasFlags
	f.attach, _ = cmd.Flags().GetStringArray("attach")
	f.page, _ = cmd.Flags().GetString("page")
	f.page = strings.TrimSpace(f.page)
	if cmd.Flags().Lookup("ask-form") != nil {
		f.askForm, _ = cmd.Flags().GetString("ask-form")
		f.askForm = strings.TrimSpace(f.askForm)
		f.askVars, _ = cmd.Flags().GetStringArray("var")
	}
	if cmd.Flags().Lookup("create-document") != nil {
		f.createDocument, _ = cmd.Flags().GetBool("create-document")
	}
	return f
}

// validate checks everything that can be checked without a server, so a bad
// combination fails before a chat is created or a file is uploaded.
func (f runExtrasFlags) validate(prompt, existingChat string, interactive, documentTarget bool) error {
	for _, p := range f.attach {
		st, err := os.Stat(p)
		if err != nil {
			return fmt.Errorf("--attach %s: %w", p, err)
		}
		if st.IsDir() {
			return fmt.Errorf("--attach %s: is a directory", p)
		}
	}
	if len(f.page) > 128 || strings.ContainsAny(f.page, "\r\n\x00") {
		return fmt.Errorf("--page must be one Page slug of at most 128 characters")
	}
	if interactive && f.metadataFlags() {
		return fmt.Errorf("--page, --ask-form and --create-document apply to one send and cannot be used with --interactive")
	}
	if len(f.askVars) > 0 && f.askForm == "" {
		return fmt.Errorf("--var answers a form field and needs --ask-form")
	}
	if f.askForm != "" {
		if existingChat == "" {
			return fmt.Errorf("--ask-form needs --chat: a form is submitted into an existing conversation")
		}
		if strings.TrimSpace(prompt) != "" {
			return fmt.Errorf("--ask-form sends the form's rendered message; drop the prompt text")
		}
		if f.page != "" || f.createDocument {
			return fmt.Errorf("--ask-form cannot be combined with --page or --create-document")
		}
	}
	if f.createDocument && f.page != "" {
		return fmt.Errorf("--create-document cannot be combined with --page: document creation requires a message without a selected Page")
	}
	if documentTarget && (f.page != "" || f.createDocument || f.askForm != "") {
		return fmt.Errorf("--document-target cannot be combined with --page, --ask-form or --create-document")
	}
	return nil
}

// prepareRunExtras uploads attachments, builds the send's metadata and returns
// the content to send. Called once the chat exists — uploads are scoped to it.
func prepareRunExtras(ctx context.Context, client *cli.Client, agentID, chatID, prompt string, f runExtrasFlags) (string, error) {
	runMessageExtras = nil
	if !f.any() {
		return prompt, nil
	}
	var paths []string
	for _, p := range f.attach {
		up, err := uploadChatAttachment(ctx, client, agentID, chatID, p)
		if err != nil {
			return "", fmt.Errorf("attach %s: %w", p, err)
		}
		if strings.TrimSpace(up.Path) == "" {
			return "", fmt.Errorf("attach %s: the server returned no attachment path", p)
		}
		paths = append(paths, cleanAttachmentPath(up.Path))
	}

	metadata := map[string]any{}
	content := composeMessageWithAttachments(prompt, paths)
	if f.askForm != "" {
		rendered, env, err := buildAskSubmission(ctx, client, agentID, chatID, f.askForm, f.askVars, paths)
		if err != nil {
			return "", err
		}
		content = rendered
		metadata[askforms.EnvelopeMetadataKey] = env
	}
	if f.page != "" {
		// Exactly one key: chatbridge.requestedPageSlug refuses any other
		// shape, and the server derives everything else (id, name, snapshot).
		metadata["page_context"] = map[string]any{"slug": f.page}
	}
	if f.createDocument {
		// The composer's shape (chat-composer.tsx): a fresh submission id per
		// send and the drafting purpose. The server decides — it requires a
		// private chat, a trusted human receipt and the feature switch.
		metadata[documents.ComposerCreationMetadataKey] = map[string]any{
			"submission_id": uuid.NewString(),
			"drafting":      true,
		}
	}
	if len(metadata) > 0 {
		runMessageExtras = metadata
	}
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("nothing to send: the message is empty")
	}
	return content, nil
}

// sendRunMessageWithExtras is sendRunMessage plus runMessageExtras. With no
// extras it IS sendRunMessage, so a plain run sends the same frame as before.
func sendRunMessageWithExtras(ws *cli.WSClient, channel, chat, prompt string, maxTurns int, targets ...map[string]any) error {
	if len(runMessageExtras) == 0 {
		return sendRunMessage(ws, channel, chat, prompt, maxTurns, targets...)
	}
	metadata := map[string]any{}
	for k, v := range runMessageExtras {
		metadata[k] = v
	}
	if len(targets) > 0 && targets[0] != nil {
		for key, value := range targets[0] {
			metadata[key] = value
		}
	}
	return ws.SendMessageWithMetadata(channel, chat, prompt, maxTurns, metadata)
}

// chatAttachmentUpload is the POST …/attachments response.
type chatAttachmentUpload struct {
	Filename  string `json:"filename" yaml:"filename"`
	Size      int64  `json:"size" yaml:"size"`
	Path      string `json:"path" yaml:"path"`
	AgentPath string `json:"agent_path" yaml:"agent_path"`
}

// uploadChatAttachment uploads one local file into (agent, chat). Shared by
// `chat attach` and `run|ask --attach` so the two cannot diverge.
func uploadChatAttachment(ctx context.Context, client *cli.Client, agentID, chatID, localPath string) (chatAttachmentUpload, error) {
	var result chatAttachmentUpload
	fh, err := os.Open(localPath)
	if err != nil {
		return result, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer fh.Close()

	// Multipart body assembled in memory: the 25 MB server-side cap keeps it
	// small, and streaming through io.Pipe buys nothing at that size.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filepath.Base(localPath))
	if err != nil {
		return result, fmt.Errorf("multipart form: %w", err)
	}
	if _, err := io.Copy(fw, fh); err != nil {
		return result, fmt.Errorf("multipart copy: %w", err)
	}
	if err := mw.Close(); err != nil {
		return result, fmt.Errorf("multipart close: %w", err)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	path := "/api/v1/agents/" + url.PathEscape(agentID) +
		"/chats/" + url.PathEscape(chatID) + "/attachments"
	resp, err := postMultipart(ctx, client, path, mw.FormDataContentType(), &buf)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return result, err
	}
	if err := cli.ReadJSON(resp, &result); err != nil {
		return result, err
	}
	return result, nil
}

// cleanAttachmentPath mirrors lib/attachment-message.ts cleanPath: control
// characters out, edges trimmed.
func cleanAttachmentPath(p string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, p))
}

// attachmentBlock is lib/attachment-message.ts attachmentBlock, byte for byte —
// the ask-form check in chatbridge compares against the same wording, so a
// drift here is a refused form, not a cosmetic difference.
func attachmentBlock(paths []string) string {
	lead := "I've attached a file to this message. The path is relative to your working directory:"
	if len(paths) > 1 {
		lead = fmt.Sprintf("I've attached %d files to this message. The paths are relative to your working directory:", len(paths))
	}
	return lead + "\n\n- " + strings.Join(paths, "\n- ")
}

// composeMessageWithAttachments is lib/attachment-message.ts
// composeMessageWithAttachments: no paths → the text unchanged; paths and no
// text → the block alone; both → text, blank line, block.
func composeMessageWithAttachments(text string, paths []string) string {
	if len(paths) == 0 {
		return text
	}
	block := attachmentBlock(paths)
	if body := strings.TrimSpace(text); body != "" {
		return body + "\n\n" + block
	}
	return block
}

// buildAskSubmission renders one of the agent's ask forms with the --var
// answers and returns the message text plus its envelope, exactly as the
// server will re-derive them (chatbridge validateAskSubmission): the server
// re-renders from the envelope's values and refuses the send if the text
// differs by a byte, so the text is rendered from the envelope, not from the
// raw flags.
func buildAskSubmission(ctx context.Context, client *cli.Client, agentID, chatID, formID string, vars, messagePaths []string) (string, askforms.Envelope, error) {
	var env askforms.Envelope
	var agent agentDetailResponse
	if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID), &agent); err != nil {
		return "", env, err
	}
	raw := ""
	if agent.AskForms != nil {
		raw = *agent.AskForms
	}
	forms, err := askforms.Parse(raw)
	if err != nil {
		return "", env, fmt.Errorf("this agent's stored ask forms are not valid: %w", err)
	}
	form, found := askforms.FormByID(forms, formID)
	if !found {
		ids := make([]string, 0, len(forms))
		for _, f := range forms {
			ids = append(ids, f.ID)
		}
		if len(ids) == 0 {
			return "", env, fmt.Errorf("agent has no ask forms configured")
		}
		return "", env, fmt.Errorf("agent has no ask form %q (available: %s)", formID, strings.Join(ids, ", "))
	}

	values, err := parseAskVars(vars)
	if err != nil {
		return "", env, err
	}
	// A file or photo field is answered with LOCAL paths; upload each and
	// answer with the agent-visible path the server returns, which is the
	// identity the envelope records (field_attachment_ids).
	attachments := map[string][]string{}
	for _, field := range form.Fields {
		if !askforms.IsAttachmentType(field.Type) {
			continue
		}
		var locals []string
		switch v := values[field.Name].(type) {
		case string:
			locals = []string{v}
		case []string:
			locals = v
		}
		var ids []string
		for _, local := range locals {
			if strings.TrimSpace(local) == "" {
				continue
			}
			up, err := uploadChatAttachment(ctx, client, agentID, chatID, local)
			if err != nil {
				return "", env, fmt.Errorf("--var %s=%s: %w", field.Name, local, err)
			}
			ids = append(ids, cleanAttachmentPath(up.Path))
		}
		if len(ids) > 0 {
			attachments[field.Name] = ids
			values[field.Name] = ids
		} else {
			delete(values, field.Name)
		}
	}

	// Refuse locally with the same validator the server runs: a missing
	// required answer is better reported before anything is sent.
	if problems := askforms.ValidateAnswers(form, values); len(problems) > 0 {
		lines := make([]string, 0, len(problems))
		for _, p := range problems {
			lines = append(lines, "  "+p.Field+": "+p.Message)
		}
		return "", env, fmt.Errorf("the form would be refused:\n%s", strings.Join(lines, "\n"))
	}
	if form.Attachment == "required" && len(attachments) == 0 && len(messagePaths) == 0 {
		return "", env, fmt.Errorf("form %q needs a document: answer its file field or pass --attach", form.Label)
	}

	env = askforms.NewEnvelope(form, uuid.NewString(), values, attachments, "")
	rendered := askforms.Render(form, askEnvelopeValues(form, env), chatID)
	env.RenderedText = rendered
	content := rendered
	if len(messagePaths) > 0 {
		env.MessageAttachmentIDs = append([]string(nil), messagePaths...)
		block := attachmentBlock(messagePaths)
		if strings.TrimSpace(content) == "" {
			content = block
		} else {
			content = strings.TrimSpace(content) + "\n\n" + block
		}
	}
	return content, env, nil
}

// askEnvelopeValues rebuilds the answers the way the server does from an
// envelope: typed values for ordinary fields (and a money field's currency),
// attachment ids for file/photo fields.
func askEnvelopeValues(form askforms.Form, env askforms.Envelope) askforms.Values {
	values := askforms.Values{}
	for _, field := range form.Fields {
		if askforms.IsAttachmentType(field.Type) {
			if ids := env.FieldAttachmentIDs[field.Name]; len(ids) > 0 {
				values[field.Name] = ids
			}
		} else if v, ok := env.Values[field.Name]; ok {
			values[field.Name] = v
		}
		if field.Type == "money" {
			key := askforms.CurrencyPlaceholder(field.Name)
			if v, ok := env.Values[key]; ok {
				values[key] = v
			}
		}
	}
	return values
}

// askSubmissionIDFromExtras is the submission id of the ask form this process
// is sending, or "" — what the stream loop matches the server's echo against.
func askSubmissionIDFromExtras() string {
	if env, ok := runMessageExtras[askforms.EnvelopeMetadataKey].(askforms.Envelope); ok {
		return env.SubmissionID
	}
	return ""
}
