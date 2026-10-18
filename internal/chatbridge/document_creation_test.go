package chatbridge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/runauthority"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/ws"
)

func TestDocumentCreationDisabledExplicitMessageIsRefused(t *testing.T) {
	bridge, _ := testBridge(t, &mockResolver{info: &ChatInfo{AgentID: "agent", AgentSlug: "agent", CrewID: "crew", WorkspaceID: "workspace", Visibility: "group"}})
	err := bridge.HandleChatMessage(t.Context(), "human", "chat", "write a document", func(ws.ChatEvent) {}, ws.ChatMessageOption{Metadata: map[string]any{"document_creation": map[string]any{"submission_id": "synthetic-original"}}})
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("disabled explicit creation not refused: %v", err)
	}
	messages, readErr := bridge.convStore.Read(t.Context(), "chat", 0, 100)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(messages) != 0 {
		t.Fatal("disabled creation persisted a human message")
	}
}

func TestDocumentCreationLiteralGrammar(t *testing.T) {
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"vytvoř dokument o rozpočtu projektu", true}, {"VYTVOŘ DOKUMENT O plánu a rizicích", true},
		{"nevytvoř dokument o plánu", false}, {"nechci vytvořit dokument o plánu", false}, {"\"vytvoř dokument o plánu\"", false},
		{"> vytvoř dokument o plánu", false}, {"`vytvoř dokument o plánu`", false}, {"vytvoř dokument o plánu\nale nevytvářej nic", false},
		{"vytvoř dokument o plánu ale nevytvářej dokument", false}, {"vytvoř dokument o plánu ne dokument", false},
		{"příklad: vytvoř dokument o plánu", false}, {"vytvoř dokument o ", false}, {"vytvoř dokument o -", false},
		{"vytvoř dokument o " + strings.Repeat("a", 201), false}, {" vytvoř dokument o plánu", false}, {"vytvoř dokument o plánu.", false},
	} {
		t.Run(tc.text, func(t *testing.T) {
			if got := literalDocumentCreation(tc.text); got != tc.want {
				t.Fatalf("recognized=%v want%v", got, tc.want)
			}
		})
	}
}

func TestDocumentCreationSendGateAndResume(t *testing.T) {
	receipt := &chataudience.Receipt{UserID: "human", ChatID: "chat", AgentID: "agent", WorkspaceID: "workspace"}
	for _, tc := range []struct {
		name       string
		enabled    bool
		opt        ws.ChatMessageOption
		selected   bool
		visibility string
		trusted    bool
		content    string
		want       bool
		denied     bool
	}{
		{name: "default off", trusted: true, visibility: "private", content: "vytvoř dokument o plánu"},
		{name: "literal", enabled: true, trusted: true, visibility: "private", content: "vytvoř dokument o plánu", want: true},
		{name: "ordinary", enabled: true, trusted: true, visibility: "private", content: "hello"},
		{name: "selected literal", enabled: true, trusted: true, visibility: "private", selected: true, content: "vytvoř dokument o plánu"},
		{name: "group literal", enabled: true, trusted: true, visibility: "group", content: "vytvoř dokument o plánu"},
		{name: "ambiguous visibility", enabled: true, trusted: true, content: "vytvoř dokument o plánu"},
		{name: "untrusted", enabled: true, visibility: "private", content: "vytvoř dokument o plánu", denied: true},
		{name: "explicit", enabled: true, trusted: true, visibility: "private", opt: ws.ChatMessageOption{Metadata: creationMetadata("explicit")}, want: true},
		{name: "explicit selected", enabled: true, trusted: true, visibility: "private", selected: true, opt: ws.ChatMessageOption{Metadata: creationMetadata("explicit")}, denied: true},
		{name: "explicit group", enabled: true, trusted: true, visibility: "group", opt: ws.ChatMessageOption{Metadata: creationMetadata("explicit")}, denied: true},
		{name: "closed shape", enabled: true, trusted: true, visibility: "private", opt: ws.ChatMessageOption{Metadata: map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": "x", "owner": "forged"}}}, denied: true},
		{name: "resume ordinary never infers", enabled: true, trusted: true, visibility: "private", content: "vytvoř dokument o plánu", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original"}},
		{name: "resume missing state", enabled: true, trusted: true, visibility: "private", opt: ws.ChatMessageOption{HumanResume: true, Metadata: creationMetadata("explicit")}, denied: true},
		{name: "resume preserved", enabled: true, trusted: true, visibility: "private", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentCreationIntent: &documents.CreationIntent{SubmissionID: "original"}}, want: true},
		{name: "resume changed", enabled: true, trusted: true, visibility: "private", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentCreationIntent: &documents.CreationIntent{SubmissionID: "original"}, Metadata: creationMetadata("changed")}, denied: true},
		{name: "resume disabled", trusted: true, visibility: "private", opt: ws.ChatMessageOption{HumanResume: true, OriginalTriggerMessageID: "original", DocumentCreationIntent: &documents.CreationIntent{SubmissionID: "original"}}, denied: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &ChatInfo{AgentID: "agent", WorkspaceID: "workspace", Visibility: tc.visibility}
			if tc.trusted {
				info.HumanAuthority = receipt
			}
			intent, err := documentCreationForSend(tc.enabled, tc.opt, info, "human", "chat", tc.content, tc.selected, "original")
			if (err != nil) != tc.denied || (intent != nil) != tc.want {
				t.Fatalf("intent=%+v err=%v", intent, err)
			}
			if tc.want && tc.opt.Metadata == nil && intent.SubmissionID != "original" {
				t.Fatal("original submission changed")
			}
		})
	}
}

func creationMetadata(id string) map[string]any {
	return map[string]any{documents.CreationMetadataKey: map[string]any{"submission_id": id}}
}

// The resolver reauthorizes against a real migrated database; its run sink
// deliberately stops at the component boundary before provider execution.
type documentCreationResolver struct {
	mockResolver
	db           *sql.DB
	runCalls     int
	metadata     map[string]any
	trigger      string
	afterResolve func()
}

func (r *documentCreationResolver) ResolveHumanChat(ctx context.Context, user, chat string) (*ChatInfo, error) {
	receipt, err := chataudience.CaptureTrusted(ctx, r.db, chat, user, chataudience.ReceiptFromContext(ctx))
	if err != nil {
		return nil, err
	}
	info := *r.info
	info.HumanAuthority = receipt
	if r.afterResolve != nil {
		r.afterResolve()
	}
	return &info, nil
}

var errDocumentCreationComponentBoundary = errors.New("synthetic run admission boundary")

func (r *documentCreationResolver) CreateRun(ctx context.Context, _, _, _, _, _ string, metadata map[string]any) error {
	r.runCalls++
	r.metadata = metadata
	r.trigger = runauthority.TriggerMessageFromContext(ctx)
	return errDocumentCreationComponentBoundary
}
func documentCreationBridgeFixture(t *testing.T) (*Bridge, *documentCreationResolver) {
	t.Helper()
	db := testutil.MigratedDB(t).DB
	for _, q := range []string{
		`INSERT INTO workspaces(id,name,slug) VALUES('creation-ws','Workspace','creation-ws')`,
		`INSERT INTO users(id,email) VALUES('creation-human','creation@example.test')`,
		`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('creation-member','creation-ws','creation-human','MEMBER')`,
		`INSERT INTO agents(id,workspace_id,name,slug) VALUES('creation-agent','creation-ws','Agent','creation-agent')`,
		`INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('creation-chat','creation-ws','creation-agent','creation-human','private')`,
	} {
		if _, err := db.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	r := &documentCreationResolver{db: db, mockResolver: mockResolver{info: &ChatInfo{AgentID: "creation-agent", AgentSlug: "creation-agent", CrewID: "creation-crew", CrewSlug: "crew", WorkspaceID: "creation-ws", Visibility: "private"}}}
	b, _ := testBridge(t, r)
	b.cfg.DocumentCreationEnabled = true
	b.convStore = conversation.NewStore(t.TempDir(), slog.Default(), conversation.WithDB(db))
	t.Cleanup(b.convStore.Close)
	b.containerCache["creation-crew"] = "synthetic-component-container"
	return b, r
}
func creationDBCount(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(t.Context(), query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDocumentCreationTrustedBridgePersistsOriginalConsent(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			content := "vytvoř dokument o rozpočtu projektu"
			opt := ws.ChatMessageOption{}
			if explicit {
				content = "Write the requested document"
				opt.Metadata = creationMetadata("explicit-submission")
			}
			var messageID string
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(e ws.ChatEvent) {
				if e.Type == "user_message" {
					metadata, _ := e.Metadata.(map[string]any)
					messageID, _ = metadata["message_id"].(string)
				}
			}, opt)
			if !errors.Is(err, errDocumentCreationComponentBoundary) || r.runCalls != 1 {
				t.Fatalf("component admission calls=%d err=%v", r.runCalls, err)
			}
			intent, ok := r.metadata["document_creation_intent"].(*documents.CreationIntent)
			if !ok || intent == nil || messageID == "" || r.trigger != messageID {
				t.Fatal("original consent/trigger missing")
			}
			want := messageID
			if explicit {
				want = "explicit-submission"
			}
			var storedID, submission, text string
			if err := r.db.QueryRow(`SELECT c.message_id,c.submission_id,m.content FROM document_creation_consents c JOIN conversation_messages m ON m.id=c.message_id`).Scan(&storedID, &submission, &text); err != nil {
				t.Fatal(err)
			}
			if storedID != messageID || submission != want || intent.SubmissionID != want || text != content {
				t.Fatal("durable original human message or consent changed")
			}
			var drafting bool
			if err := r.db.QueryRow(`SELECT drafting_allowed FROM document_creation_consents WHERE message_id=?`, messageID).Scan(&drafting); err != nil {
				t.Fatal(err)
			}
			if drafting != !explicit || intent.Drafting != drafting {
				t.Fatal("literal creation or explicit one-shot purpose changed")
			}
		})
	}
}

func TestDocumentCreationProvisioningPreservesOriginalState(t *testing.T) {
	b, r := documentCreationBridgeFixture(t)
	r.info.DevcontainerConfig = `{"image":"x","features":{"ghcr.io/devcontainers/features/go:1":{}}}`
	enq := &stubEnqueuer{resStarted: true}
	b.SetProvisioningEnqueuer(enq)
	content := "vytvoř dokument o plánu"
	err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(ws.ChatEvent) {})
	if !errors.Is(err, ws.ErrCrewProvisioning) || !enq.attachCalled {
		t.Fatalf("provisioning: %v", err)
	}
	original := enq.attachMsg.Opts
	if original.OriginalTriggerMessageID == "" || original.DocumentCreationIntent == nil || original.DocumentCreationIntent.SubmissionID != original.OriginalTriggerMessageID || original.HumanAuthority == nil {
		t.Fatal("pending send lost original server identity")
	}
	if creationDBCount(t, r.db, `SELECT COUNT(*) FROM document_creation_consents`) != 0 {
		t.Fatal("deferred message minted premature consent")
	}
	r.info.DevcontainerConfig = ""
	r.info.CLIAdapter = ""
	original.HumanResume = true
	err = b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", content, func(ws.ChatEvent) {}, original)
	if !errors.Is(err, errDocumentCreationComponentBoundary) || r.trigger != original.OriginalTriggerMessageID {
		t.Fatalf("resume replaced original identity: %v", err)
	}
	var messageID, submission string
	if err := r.db.QueryRow(`SELECT message_id,submission_id FROM document_creation_consents`).Scan(&messageID, &submission); err != nil {
		t.Fatal(err)
	}
	if messageID != original.OriginalTriggerMessageID || submission != messageID {
		t.Fatal("resume minted another submission")
	}
	var drafting bool
	if err := r.db.QueryRow(`SELECT drafting_allowed FROM document_creation_consents WHERE message_id=?`, messageID).Scan(&drafting); err != nil || !drafting || !original.DocumentCreationIntent.Drafting {
		t.Fatalf("resume lost the original drafting purpose: %v", err)
	}
}

func TestDocumentCreationTrustedFailureStopsBeforeRun(t *testing.T) {
	for _, fault := range []string{"late member revocation", "consent SQL failure", "selected page", "group explicit", "missing pending intent"} {
		t.Run(fault, func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			opt := ws.ChatMessageOption{Metadata: creationMetadata("synthetic-original")}
			switch fault {
			case "late member revocation":
				r.afterResolve = func() {
					if _, err := r.db.Exec(`DELETE FROM workspace_members WHERE id='creation-member'`); err != nil {
						t.Fatal(err)
					}
				}
			case "consent SQL failure":
				if _, err := r.db.Exec(`CREATE TRIGGER synthetic_consent_fault BEFORE INSERT ON document_creation_consents BEGIN SELECT RAISE(ABORT,'synthetic consent fault'); END`); err != nil {
					t.Fatal(err)
				}
			case "selected page":
				opt.Metadata["page_context"] = map[string]any{"slug": "selected"}
			case "group explicit":
				r.info.Visibility = "group"
			case "missing pending intent":
				receipt, err := chataudience.CaptureTrusted(t.Context(), r.db, "creation-chat", "creation-human", nil)
				if err != nil {
					t.Fatal(err)
				}
				opt.HumanAuthority = receipt
				opt.HumanResume = true
			}
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", "Write a document", func(ws.ChatEvent) {}, opt)
			if err == nil || r.runCalls != 0 {
				t.Fatalf("invalid creation reached run: calls%d err%v", r.runCalls, err)
			}
			if creationDBCount(t, r.db, `SELECT COUNT(*) FROM document_creation_consents`) != 0 {
				t.Fatal("failed creation retained consent")
			}
			if fault == "consent SQL failure" && !errors.Is(err, conversation.ErrDocumentCreationConsentUnconfirmed) {
				t.Fatalf("saved transcript failure lost typed error: %v", err)
			}
			if fault == "consent SQL failure" {
				messages, readErr := b.convStore.Read(t.Context(), "creation-chat", 0, 100)
				if readErr != nil || len(messages) != 1 || messages[0].Content != "Write a document" {
					t.Fatalf("saved transcript missing after SQL failure: %+v %v", messages, readErr)
				}
			}
		})
	}
}

func TestDocumentCreationOrdinaryChatRemainsOrdinary(t *testing.T) {
	for _, tc := range []struct {
		name, content, visibility string
		enabled                   bool
		runCalls                  int
	}{
		{"disabled literal", "vytvoř dokument o plánu", "private", false, 1},
		{"enabled ordinary", "hello", "private", true, 1},
		{"quoted command", "\"vytvoř dokument o plánu\"", "private", true, 1},
		{"group literal", "vytvoř dokument o plánu", "group", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, r := documentCreationBridgeFixture(t)
			b.cfg.DocumentCreationEnabled = tc.enabled
			r.info.Visibility = tc.visibility
			err := b.HandleChatMessage(t.Context(), "creation-human", "creation-chat", tc.content, func(ws.ChatEvent) {})
			if r.runCalls != tc.runCalls || (tc.runCalls == 1 && !errors.Is(err, errDocumentCreationComponentBoundary)) || (tc.runCalls == 0 && err != nil) {
				t.Fatalf("ordinary chat changed: calls%d err%v", r.runCalls, err)
			}
			if _, ok := r.metadata["document_creation_intent"]; ok {
				t.Fatal("ordinary chat promoted creation")
			}
			if creationDBCount(t, r.db, `SELECT COUNT(*) FROM document_creation_consents`) != 0 {
				t.Fatal("ordinary chat minted consent")
			}
			messages, err := b.convStore.Read(t.Context(), "creation-chat", 0, 100)
			if err != nil || len(messages) != 1 || messages[0].Content != tc.content {
				t.Fatalf("ordinary transcript changed: %+v %v", messages, err)
			}
		})
	}
}
