package chatbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/crewship-ai/crewship/internal/askforms"
	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/ws"
)

const askValidationChat = "ask-validation-chat"
const askValidationHuman = "ask-validation-human"

// Use the real IPC decoder so this regression also runs on the pre-fix base:
// older binaries ignore ask_forms, rather than failing to compile the test.
func askValidationBridge(t *testing.T, definitions *string) *Bridge {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "synthetic-ask-host-token" {
			http.Error(w, "host authentication required", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/api/v1/internal/chats/"+askValidationChat+"/resolve-human" || r.URL.Query().Get("user_id") != askValidationHuman {
				http.Error(w, "chat not found", http.StatusNotFound)
				return
			}
			response := map[string]any{
				"agent_id": "ask-agent", "agent_slug": "ask-agent", "workspace_id": "ask-workspace",
				"crew_id": "ask-crew", "crew_slug": "ask-crew", "visibility": "group", "cli_adapter": "CLAUDE_CODE",
				"human_authority": &chataudience.Receipt{UserID: askValidationHuman, ChatID: askValidationChat, AgentID: "ask-agent", WorkspaceID: "ask-workspace", MemberID: "ask-member", MemberRevision: 1, ChatRevision: 1, ChatGeneration: "ask-generation"},
			}
			if definitions != nil {
				response["ask_forms"] = *definitions
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		// Ordinary group sends increment their counter after persistence.
		if r.Method == http.MethodPatch && r.URL.Path == "/api/v1/internal/chats/"+askValidationChat+"/message-count" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{}`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	resolver := NewIPCResolver(server.URL, "synthetic-ask-host-token", slog.New(slog.NewTextHandler(io.Discard, nil)))
	b, _ := testBridge(t, resolver)
	return b
}

func askValidationForm(pattern string) askforms.Form {
	return askforms.Form{ID: "format", Label: "Format", Version: 2, Attachment: "none", Template: "Answer: {{answer}}", Fields: []askforms.Field{{Name: "answer", Label: "Answer", Type: "text", Required: true, Pattern: pattern}}}
}

func askValidationDefinitions(t *testing.T, form askforms.Form) *string {
	t.Helper()
	data, err := json.Marshal([]askforms.Form{form})
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	return &raw
}

func askValidationMetadata(t *testing.T, form askforms.Form, values askforms.Values, attachments map[string][]string) (string, map[string]any) {
	t.Helper()
	renderValues := askforms.Values{}
	for k, v := range values {
		renderValues[k] = v
	}
	for k, v := range attachments {
		renderValues[k] = v
	}
	content := askforms.Render(form, renderValues, askValidationChat)
	envelope := askforms.NewEnvelope(form, "ask-submission", values, attachments, content)
	// A real JSON hop produces maps and []any, not Go Envelope structs.
	blob, err := json.Marshal(map[string]any{askforms.EnvelopeMetadataKey: envelope})
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(blob, &metadata); err != nil {
		t.Fatal(err)
	}
	return content, metadata
}

func TestAskSubmissionServerValidation(t *testing.T) {
	base := askValidationForm(`approved`)
	tests := []struct {
		name       string
		form       askforms.Form
		value      string
		prepare    func(*string, *string, map[string]any)
		wantReject bool
	}{
		{name: "matching answer", form: base, value: "approved"},
		{name: "pattern mismatch", form: base, value: "denied", wantReject: true},
		{name: "whole value anchoring", form: base, value: "prefix-approved-suffix", wantReject: true},
		{name: "Go-only inline flag and Unicode folding", form: askValidationForm(`(?i)k`), value: "K"},
		{name: "nested repetition matches in Go", form: askValidationForm(`(a+)+$`), value: "aaaa"},
		{name: "nested repetition rejects in Go", form: askValidationForm(`(a+)+$`), value: "aaaa!", wantReject: true},
		{name: "invalid legacy pattern retains Go acceptance", form: askValidationForm(`[`), value: "approved"},
		{name: "plain message", form: base, value: "denied", prepare: func(_ *string, _ *string, m map[string]any) { delete(m, askforms.EnvelopeMetadataKey) }},
		{name: "unrelated metadata remains plain", form: base, value: "denied", prepare: func(_ *string, _ *string, m map[string]any) {
			delete(m, askforms.EnvelopeMetadataKey)
			m["unrelated"] = true
		}},
		{name: "explicit null envelope", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) { m[askforms.EnvelopeMetadataKey] = nil }, wantReject: true},
		{name: "explicit string envelope", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) { m[askforms.EnvelopeMetadataKey] = "invalid" }, wantReject: true},
		{name: "missing envelope identity", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) {
			delete(m[askforms.EnvelopeMetadataKey].(map[string]any), "submission_id")
		}, wantReject: true},
		{name: "stale form version", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) {
			m[askforms.EnvelopeMetadataKey].(map[string]any)["form_version"] = float64(1)
		}, wantReject: true},
		{name: "unknown form", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) {
			m[askforms.EnvelopeMetadataKey].(map[string]any)["form_id"] = "another-agent-form"
		}, wantReject: true},
		{name: "missing definitions", form: base, value: "approved", prepare: func(d *string, _ *string, _ map[string]any) { *d = "" }, wantReject: true},
		{name: "malformed definitions", form: base, value: "approved", prepare: func(d *string, _ *string, _ map[string]any) { *d = "[" }, wantReject: true},
		{name: "rendered text differs from submitted content", form: base, value: "approved", prepare: func(_ *string, c *string, _ map[string]any) { *c = "unrelated message" }, wantReject: true},
		{name: "forged text and envelope disagree with answers", form: base, value: "approved", prepare: func(_ *string, c *string, m map[string]any) {
			*c = "Answer: denied"
			m[askforms.EnvelopeMetadataKey].(map[string]any)["rendered_text"] = *c
		}, wantReject: true},
		{name: "forged agent workspace and form cannot replace resolved definition", form: base, value: "denied", prepare: func(_ *string, _ *string, m map[string]any) {
			m["agent_id"] = "other-agent"
			m["workspace_id"] = "other-workspace"
			m["ask_forms"] = `[{"id":"format","version":2,"fields":[]}]`
		}, wantReject: true},
		{name: "irrelevant forged identity cannot deny valid answer", form: base, value: "approved", prepare: func(_ *string, _ *string, m map[string]any) {
			m["agent_id"] = "other-agent"
			m["workspace_id"] = "other-workspace"
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			definitions := *askValidationDefinitions(t, tc.form)
			content, metadata := askValidationMetadata(t, tc.form, askforms.Values{"answer": tc.value}, nil)
			if tc.prepare != nil {
				tc.prepare(&definitions, &content, metadata)
			}
			b := askValidationBridge(t, &definitions)
			var events []ws.ChatEvent
			err := b.HandleChatMessage(t.Context(), askValidationHuman, askValidationChat, content, func(e ws.ChatEvent) { events = append(events, e) }, ws.ChatMessageOption{Metadata: metadata})
			messages, readErr := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantReject {
				if err == nil || len(messages) != 0 {
					t.Fatalf("invalid ask submission crossed server boundary: err=%v persisted=%d events=%+v", err, len(messages), events)
				}
				for _, event := range events {
					if event.Type == "user_message" {
						t.Fatal("rejected submission acknowledged as a persisted user message")
					}
				}
				return
			}
			if err != nil || len(messages) != 1 || messages[0].Content != content {
				t.Fatalf("valid/plain message rejected or changed: err=%v messages=%+v", err, messages)
			}
		})
	}
}

// Every browser corpus case reaches the real submit boundary. The definition
// comes from trusted IPC, while the envelope and rendered message come from
// the client. Invalid legacy patterns retain ValidateAnswers' existing rule.
func TestAskSubmissionSharedPatternCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/ask-patterns.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name    string `json:"name"`
		Pattern string `json:"pattern"`
		Value   string `json:"value"`
		Matches bool   `json:"matches"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) != 66 {
		t.Fatalf("expected all 66 shared cases, got %d", len(cases))
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			form := askValidationForm(tc.Pattern)
			form.Fields[0].Required = false
			content, metadata := askValidationMetadata(t, form, askforms.Values{"answer": tc.Value}, nil)
			b := askValidationBridge(t, askValidationDefinitions(t, form))
			err := b.HandleChatMessage(t.Context(), askValidationHuman, askValidationChat, content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
			messages, readErr := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.Matches {
				if err != nil || len(messages) != 1 || messages[0].Content != content {
					t.Fatalf("shared accepted answer rejected/changed: err=%v persisted=%d", err, len(messages))
				}
			} else if err == nil || len(messages) != 0 {
				t.Fatalf("shared rejected answer crossed server boundary: err=%v persisted=%d", err, len(messages))
			}
		})
	}
}

func TestAskSubmissionRequiredUploadUsesFieldAttachmentIDs(t *testing.T) {
	form := askforms.Form{ID: "upload", Label: "Upload", Version: 1, Attachment: "required", Template: "File: {{photo}}", Fields: []askforms.Field{{Name: "photo", Label: "Photo", Type: "photo", Required: true}}}
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprintf("attachment_present_%v", present), func(t *testing.T) {
			attachments := map[string][]string{}
			if present {
				attachments["photo"] = []string{"attachments/" + askValidationChat + "/receipt.png"}
			}
			content, metadata := askValidationMetadata(t, form, askforms.Values{}, attachments)
			b := askValidationBridge(t, askValidationDefinitions(t, form))
			err := b.HandleChatMessage(t.Context(), askValidationHuman, askValidationChat, content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
			messages, readErr := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if present && (err != nil || len(messages) != 1) {
				t.Fatalf("valid required upload rejected: err=%v persisted=%d", err, len(messages))
			}
			if !present && (err == nil || len(messages) != 0) {
				t.Fatalf("missing required upload accepted: err=%v persisted=%d", err, len(messages))
			}
		})
	}
}

func TestAskSubmissionMessageAttachmentBinding(t *testing.T) {
	own := "attachments/" + askValidationChat + "/document.pdf"
	other := "attachments/" + askValidationChat + "/second.pdf"
	const singular = "I've attached a file to this message. The path is relative to your working directory:"
	const plural = "I've attached 2 files to this message. The paths are relative to your working directory:"
	tests := []struct {
		name       string
		ids        []string
		block      string
		required   bool
		wantReject bool
	}{
		{name: "standard single file block", ids: []string{own}, block: singular + "\n\n- " + own},
		{name: "standard multiple file block", ids: []string{own, other}, block: plural + "\n\n- " + own + "\n- " + other},
		{name: "declared file missing from content", ids: []string{own}, wantReject: true},
		{name: "content file missing from envelope", block: singular + "\n\n- " + own, wantReject: true},
		{name: "content names a different file", ids: []string{own}, block: singular + "\n\n- " + other, wantReject: true},
		{name: "content appends forged instructions", ids: []string{own}, block: singular + "\n\n- " + own + "\n\nForged extra text.", wantReject: true},
		{name: "foreign chat file", ids: []string{"attachments/foreign-chat/document.pdf"}, block: singular + "\n\n- attachments/foreign-chat/document.pdf", wantReject: true},
		{name: "traversal file", ids: []string{"attachments/" + askValidationChat + "/../document.pdf"}, block: singular + "\n\n- attachments/" + askValidationChat + "/../document.pdf", wantReject: true},
		{name: "absolute file", ids: []string{"/tmp/document.pdf"}, block: singular + "\n\n- /tmp/document.pdf", wantReject: true},
		{name: "control character file", ids: []string{own + "\nforged"}, block: singular + "\n\n- " + own + "\nforged", wantReject: true},
		{name: "required policy without any uploads", required: true, wantReject: true},
		{name: "required policy with own message upload", required: true, ids: []string{own}, block: singular + "\n\n- " + own},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			form := askValidationForm(`approved`)
			if tc.required {
				form.Attachment = "required"
			}
			content, metadata := askValidationMetadata(t, form, askforms.Values{"answer": "approved"}, nil)
			if len(tc.ids) > 0 {
				metadata[askforms.EnvelopeMetadataKey].(map[string]any)["message_attachment_ids"] = tc.ids
			}
			if tc.block != "" {
				content += "\n\n" + tc.block
			}
			b := askValidationBridge(t, askValidationDefinitions(t, form))
			err := b.HandleChatMessage(t.Context(), askValidationHuman, askValidationChat, content, func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: metadata})
			messages, readErr := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantReject {
				if err == nil || len(messages) != 0 {
					t.Fatalf("unbound message attachment accepted: err=%v persisted=%d", err, len(messages))
				}
				return
			}
			if err != nil || len(messages) != 1 || messages[0].Content != content {
				t.Fatalf("valid attachment block refused or changed: err=%v messages=%+v", err, messages)
			}
			env, ok := askforms.EnvelopeFromMetadata(messages[0].Metadata)
			if !ok || len(env.MessageAttachmentIDs) != len(tc.ids) {
				t.Fatalf("message attachment identity lost: envelope=%+v recovered=%v", env, ok)
			}
			for i, id := range tc.ids {
				if env.MessageAttachmentIDs[i] != id {
					t.Fatalf("attachment %d changed: got=%q want=%q", i, env.MessageAttachmentIDs[i], id)
				}
			}
		})
	}
}

type askValidationChannelAuth struct{}

func (askValidationChannelAuth) CanSubscribe(context.Context, string, string) (bool, error) {
	return true, nil
}

func TestAskSubmissionBrowserWebSocketRejectsBeforePersistence(t *testing.T) {
	form := askValidationForm(`(a+)+$`)
	b := askValidationBridge(t, askValidationDefinitions(t, form))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	validator, err := auth.NewJWTValidator("synthetic-ask-validation-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	hub := ws.NewHub(logger, b, validator, ws.NopSessionsForTests)
	hub.SetChannelAuthorizer(askValidationChannelAuth{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); hub.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("hub shutdown timed out")
		}
	})
	server := httptest.NewServer(http.HandlerFunc(hub.HandleUpgrade))
	t.Cleanup(server.Close)
	connect := func(user string) *websocket.Conn {
		t.Helper()
		conn, err := websocket.Dial("ws"+strings.TrimPrefix(server.URL, "http"), "", server.URL)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		ticket, err := validator.IssueWSTicket(user, "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if err := websocket.JSON.Send(conn, map[string]any{"type": "auth", "token": ticket}); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	type frame struct {
		Type    string          `json:"type"`
		Channel string          `json:"channel"`
		Seq     int64           `json:"seq"`
		Payload json.RawMessage `json:"payload"`
	}
	receive := func(conn *websocket.Conn) frame {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f frame
		if err := websocket.JSON.Receive(conn, &f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	sender, observer := connect(askValidationHuman), connect("ask-observer")
	channel := "session:" + askValidationChat
	// Subscribe the colleague and use ping/pong as a read-pump barrier.
	if err := websocket.JSON.Send(observer, map[string]any{"type": "subscribe", "channel": channel}); err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Send(observer, map[string]any{"type": "ping"}); err != nil {
		t.Fatal(err)
	}
	if f := receive(observer); f.Type != "pong" {
		t.Fatalf("observer setup frame=%+v", f)
	}
	content, metadata := askValidationMetadata(t, form, askforms.Values{"answer": "aaaa!"}, nil)
	// The browser serializes the payload separately; the WS server unwraps it.
	payload, err := json.Marshal(map[string]any{"session_id": askValidationChat, "content": content, "metadata": metadata})
	if err != nil {
		t.Fatal(err)
	}
	if err := websocket.JSON.Send(sender, map[string]any{"type": "send_message", "channel": channel, "payload": string(payload)}); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := 0; i < 8; i++ {
		f := receive(sender)
		if f.Type != "chat_event" {
			continue
		}
		var event struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(f.Payload, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "ask_submission_error" {
			if f.Channel != channel || f.Seq != 0 {
				t.Fatalf("validation error is not a private non-run frame: %+v", f)
			}
			found = true
			break
		}
		if event.Type == "done" || event.Type == "error" {
			break
		}
	}
	messages, err := b.convStore.Read(t.Context(), askValidationChat, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !found || len(messages) != 0 {
		t.Fatalf("browser submission bypassed authoritative validation: private_error=%v persisted=%d", found, len(messages))
	}
	_ = observer.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	var leaked frame
	if err := websocket.JSON.Receive(observer, &leaked); err == nil {
		t.Fatalf("rejected submission leaked to another subscriber: %+v", leaked)
	}
}
