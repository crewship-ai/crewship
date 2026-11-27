package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/work"
)

type craftNativeAgentWorkInput struct {
	Application    string               `json:"application_id"`
	PageID         string               `json:"page_id"`
	ChatID         string               `json:"chat_id"`
	MessageID      string               `json:"message_id"`
	WholeDocument  bool                 `json:"whole_document"`
	Instruction    string               `json:"instruction"`
	Original       chataudience.Receipt `json:"original_human"`
	Epochs         [6]string            `json:"epochs"`
	WorkspaceEpoch string               `json:"workspace_epoch"`
	Mode           string               `json:"mode"`
	ModeExplicit   bool                 `json:"mode_explicit"`
}

func (m *craftNativeAgentManager) produceTask(ctx context.Context, slug string, input craftNativeAgentTaskRequest) (work.Receipt, error) {
	var zero work.Receipt
	if m == nil || !m.enabled || m.human == nil || !m.human.enabled || m.human.clock == nil || m.documents == nil || !input.WholeDocument || !mooring.ValidWireIdentifier(input.ChatID) || !mooring.ValidWireIdentifier(input.MessageID) {
		return zero, errCraftNativeAgentDenied
	}
	source, err := craftNativeHuman(ctx)
	if err != nil {
		return zero, err
	}
	ws := WorkspaceIDFromContext(ctx)
	tx, err := m.human.pages.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	// Acquire the existing SQL writer before original consent/epoch capture.
	if _, err = tx.ExecContext(ctx, `UPDATE mooring_session_writer_fence SET seq=seq WHERE id=1`); err != nil {
		return zero, err
	}
	// SQL writer precedes the registry mutex: publication callbacks also inspect
	// this registry inside their writer, so the inverse order could deadlock.
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	rec, err := m.human.pages.loadPageForRead(ctx, ws, slug)
	if err != nil {
		return zero, err
	}
	scope := mooring.EditLeaseScope{WorkspaceID: ws, ArtifactID: rec.ID}
	proof, err := m.human.pageAuthority(ctx, tx, source, scope, true)
	if err != nil {
		return zero, err
	}
	// From here the requester can see and edit this Page, so a refusal may
	// name its reason (#5596). Nothing above this line may: a caller without
	// Page access gets one generic answer, whatever the state behind it.
	// The reasons do disclose state to this authorized editor: with their own
	// recorded instruction they learn whether the workspace agent artifact
	// policy is on and whether it allows that agent's crew.
	refuse := func(reason string) error {
		if reason == "" {
			return errCraftNativeAgentDenied
		}
		return craftNativeAgentRefusal{reason: reason}
	}
	if len(m.entries) >= 128 {
		return zero, refuse(craftNativeAgentReasonCapacity)
	}
	if application, ok := m.documents.resolveDocument(proof.application, proof.pin); !ok || application.application.Store.Format == mooring.NativeFormatWorkbook {
		return zero, mooring.ErrNativeDisabled
	}
	original, err := chataudience.CaptureTrustedQuery(ctx, tx, input.ChatID, source.UserID, nil)
	if err != nil {
		return zero, errCraftNativeAgentDenied
	}
	if original == nil || original.WorkspaceID != ws || original.OriginalCredential() != source {
		return zero, refuse(craftNativeAgentReasonChatUnavailable)
	}
	// Only a recorded instruction in the author's private chat: an ordinary
	// turn has already reached agent logs and the journal, and a group chat
	// shows the instruction to people without Page access (#5009).
	var crew, instruction string
	err = tx.QueryRowContext(ctx, `SELECT a.crew_id,m.content FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL JOIN conversation_messages m ON m.agent_id=a.id AND m.id=? AND m.session_id=? AND m.author_user_id=? AND m.role='user' AND m.native_craft_instruction=1 JOIN chats ch ON ch.id=m.session_id AND ch.visibility='private' AND ch.created_by=m.author_user_id WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL AND length(CAST(m.content AS BLOB))<=?`, input.MessageID, input.ChatID, source.UserID, original.AgentID, ws, mooring.MaxRPCFrame).Scan(&crew, &instruction)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, refuse(craftNativeAgentInstructionReason(ctx, tx, input, source.UserID, original.AgentID, ws))
	}
	if err != nil {
		return zero, errCraftNativeAgentDenied
	}
	if !mooring.ValidWireIdentifier(crew) || !utf8.ValidString(instruction) || strings.TrimSpace(instruction) == "" {
		return zero, refuse(craftNativeAgentReasonInstructionUnusable)
	}
	// The workspace agent artifact policy is the ceiling for every agent write
	// to an artifact; whole-document consent works only inside it (#4681).
	if !m.human.pages.artifactAgentPolicyAllows(ctx, ws, crew) {
		return zero, refuse(craftNativeAgentPolicyReason(ctx, tx, ws))
	}
	// R71 §0.14/1 (#5235): the author's own Edit is lent to the agent only
	// while the author owns the document; otherwise the owner must have given
	// this agent write. A workspace role or an Edit grant is not delegable.
	// Checked after the recorded instruction and the policy (#5596), so a
	// non-owner editor learns about an agent's grant only for an agent they
	// already hold their own recorded instruction with.
	if !m.human.pages.humanAgentMayWrite(ctx, ws, rec.ID, source.UserID, original.AgentID) {
		return zero, refuse(craftNativeAgentReasonNotOwnerOrDelegated)
	}
	var duplicate bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE workspace_id=? AND source='chat' AND source_ref=? AND domain_kind=? AND domain_id=? AND agent_id=?)`, ws, input.MessageID, craftNativeAgentDomain, rec.ID, original.AgentID).Scan(&duplicate); err != nil {
		return zero, errCraftNativeAgentDenied
	}
	if duplicate {
		return zero, refuse(craftNativeAgentReasonDuplicateTask)
	}
	boot, now, err := m.human.clock.Now()
	if err != nil || boot == "" || now < 0 {
		return zero, refuse(craftNativeAgentReasonClockUnavailable)
	}
	ttl := time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < ttl {
		ttl = time.Until(proof.expiry)
	}
	if ttl <= 0 {
		return zero, refuse(craftNativeAgentReasonCredentialExpiring)
	}
	if now > time.Duration(math.MaxInt64)-ttl {
		return zero, refuse(craftNativeAgentReasonClockUnavailable)
	}
	g := craftNativeAgentGrant{Scope: scope, Application: proof.application, Source: source, Original: *original, MessageID: input.MessageID, AgentID: original.AgentID, CrewID: crew, Proof: proof, Boot: boot, Deadline: now + ttl, Instruction: instruction}
	g.Mode, g.ModeExplicit = craftNativeAgentMode(input.Mode, craftNativePersonalSpace(rec, source.UserID))
	g.Epochs, err = craftNativeAgentEpochs(ctx, tx, g, true)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(craftNativeAgentWorkInput{g.Application, rec.ID, input.ChatID, input.MessageID, true, instruction, *original, g.Epochs, proof.workspaceEpoch, g.Mode, g.ModeExplicit})
	if err != nil {
		return zero, errCraftNativeAgentDenied
	}
	if len(raw) > mooring.MaxRPCFrame {
		return zero, refuse(craftNativeAgentReasonTaskTooLarge)
	}
	sum := sha256.Sum256(raw)
	accepted, err := work.NewStore(m.human.pages.db).AcceptTx(ctx, tx, work.AcceptRequest{WorkspaceID: ws, Source: work.SourceChat, SourceRef: input.MessageID, DomainKind: craftNativeAgentDomain, DomainID: rec.ID, AgentID: original.AgentID, CrewID: crew, SessionID: input.ChatID, Class: work.ClassChat, AuthorizedByUserID: source.UserID, AuthorizedScope: "pages:write", InputJSON: string(raw), InputSHA256: hex.EncodeToString(sum[:]), TargetRevision: "whole-native", DeadlineAt: time.Now().Add(ttl)})
	if err != nil {
		return zero, err
	}
	id, err := craftNativeOpaque()
	if err != nil {
		return zero, err
	}
	if err = m.checkOriginalGrant(ctx, tx, g); err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	m.entries[accepted.WorkID] = &craftNativeAgentEntry{grant: g, workID: accepted.WorkID, inputSHA256: hex.EncodeToString(sum[:]), id: id, seal: m.seal}
	return accepted, nil
}
