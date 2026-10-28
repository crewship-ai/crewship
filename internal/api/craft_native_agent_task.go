package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	Epochs         [4]string            `json:"epochs"`
	WorkspaceEpoch string               `json:"workspace_epoch"`
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
	if len(m.entries) >= 128 {
		return zero, errCraftNativeAgentDenied
	}
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
	if _, ok := m.documents.applications[proof.application]; !ok {
		return zero, mooring.ErrNativeDisabled
	}
	original, err := chataudience.CaptureTrustedQuery(ctx, tx, input.ChatID, source.UserID, nil)
	if err != nil || original == nil || original.WorkspaceID != ws || original.OriginalCredential() != source {
		return zero, errCraftNativeAgentDenied
	}
	var crew, instruction string
	err = tx.QueryRowContext(ctx, `SELECT a.crew_id,m.content FROM agents a JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL JOIN conversation_messages m ON m.agent_id=a.id AND m.id=? AND m.session_id=? AND m.author_user_id=? AND m.role='user' WHERE a.id=? AND a.workspace_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL AND length(CAST(m.content AS BLOB))<=?`, input.MessageID, input.ChatID, source.UserID, original.AgentID, ws, mooring.MaxRPCFrame).Scan(&crew, &instruction)
	if err != nil || !mooring.ValidWireIdentifier(crew) || !utf8.ValidString(instruction) || strings.TrimSpace(instruction) == "" {
		return zero, errCraftNativeAgentDenied
	}
	var duplicate bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM work_items WHERE workspace_id=? AND source='chat' AND source_ref=? AND domain_kind=? AND domain_id=? AND agent_id=?)`, ws, input.MessageID, craftNativeAgentDomain, rec.ID, original.AgentID).Scan(&duplicate); err != nil || duplicate {
		return zero, errCraftNativeAgentDenied
	}
	boot, now, err := m.human.clock.Now()
	if err != nil || boot == "" || now < 0 {
		return zero, errCraftNativeAgentDenied
	}
	ttl := time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < ttl {
		ttl = time.Until(proof.expiry)
	}
	if ttl <= 0 || now > time.Duration(math.MaxInt64)-ttl {
		return zero, errCraftNativeAgentDenied
	}
	g := craftNativeAgentGrant{Scope: scope, Application: proof.application, Source: source, Original: *original, MessageID: input.MessageID, AgentID: original.AgentID, CrewID: crew, Proof: proof, Boot: boot, Deadline: now + ttl, Instruction: instruction}
	g.Epochs, err = craftNativeAgentEpochs(ctx, tx, g, true)
	if err != nil {
		return zero, err
	}
	raw, err := json.Marshal(craftNativeAgentWorkInput{g.Application, rec.ID, input.ChatID, input.MessageID, true, instruction, *original, g.Epochs, proof.workspaceEpoch})
	if err != nil || len(raw) > mooring.MaxRPCFrame {
		return zero, errCraftNativeAgentDenied
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
