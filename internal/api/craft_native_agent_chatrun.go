package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/crewship-ai/crewship/internal/craftapps"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// Sheet tools for an ordinary chat run (#5317 D1, native Sheet).
//
// A person attaches their own private Sheet to a message of their private chat
// with an agent. With the workspace flag on, that run may read and change the
// Sheet, and nothing else, through the same tool operations a native Craft task
// uses (acquire, renew, apply, release, propose): same lease, same compare and
// swap publication, same receipts. Only the authority differs: it is derived on
// every call from the run's own principal and scope, never from a task grant.
//
// Writes are narrower than reads in D1: the run must reach the Sheet as
// attached to its message (not standing, folder, created or consented reach)
// and its human must own it in their personal space. Nothing about the Sheet
// is chosen by the agent: the tools carry no page.

const craftNativeChatRunPrefix = "chatrun:"

// The reasons a chat run's Sheet tool is refused, a closed list the UI relies
// on. A Sheet that is not attached to this run, is not the human's own or does
// not exist all read as not_attached: nothing is said about another Sheet.
const (
	CraftSheetReasonFeatureOff      = "feature_off"
	CraftSheetReasonAgentPolicy     = "agent_policy"
	CraftSheetReasonNotAttached     = "not_attached"
	CraftSheetReasonHolderHuman     = "holder_human"
	CraftSheetReasonProposal        = "proposal_required"
	CraftSheetReasonStale           = "stale"
	CraftSheetReasonLeaseExpired    = "lease_expired"
	CraftSheetReasonTooLarge        = "too_large"
	CraftSheetReasonFormula         = "formula_not_allowed"
	CraftSheetReasonInvalid         = "invalid_request"
	CraftSheetReasonEngineFailed    = "engine_failed"
	CraftSheetReasonFidelityLoss    = "fidelity_loss"
	CraftSheetReasonBusy            = "busy"
	CraftSheetReasonConsentStale    = "consent_stale"
	CraftSheetReasonNotUndoable     = "not_undoable"
	craftSheetReasonUnavailableText = "Native Craft agents are not available."
)

var craftSheetReasonText = map[string]string{
	CraftSheetReasonNotUndoable:  "The Sheet change could not be made undoable, so nothing was changed. Try a smaller range or leave those cells unchanged.",
	CraftSheetReasonFeatureOff:   "Agent access to Sheets is not turned on for this workspace.",
	CraftSheetReasonAgentPolicy:  "This workspace does not let this agent's crew change artifacts.",
	CraftSheetReasonNotAttached:  "No Sheet you may use is attached to this message, so nothing was read or changed. Do not work on a copy of the file with Python or another tool, and do not create a new workbook: tell the person, and ask them to attach the Sheet to their message with agent edits turned on, then send it again.",
	CraftSheetReasonHolderHuman:  "A person is editing this Sheet right now.",
	CraftSheetReasonProposal:     "This Sheet may only receive a proposal, not a direct change.",
	CraftSheetReasonStale:        "The Sheet changed since it was read; read it again.",
	CraftSheetReasonLeaseExpired: "Your hold on the Sheet expired and someone else took it; nothing was changed. Acquire the Sheet again, read it, then send the change.",
	CraftSheetReasonTooLarge:     "The request is too large.",
	CraftSheetReasonFormula:      "Formulas are not allowed in this request.",
	CraftSheetReasonInvalid:      "The request is not a valid Sheet operation.",
	CraftSheetReasonEngineFailed: "The Sheet engine could not complete the request.",
	CraftSheetReasonBusy:         "The Sheet engine stayed busy with other Sheets while this waited for its turn; nothing was changed. Tell the person the Sheet is waiting for the engine, then send the same request again (same request_id) shortly.",
	CraftSheetReasonConsentStale: "The Sheet changed after the person allowed you to change it; nothing was changed. Ask them to allow it again.",
}

// craftNativeChatRefusal carries one of the reasons above out of the binding.
type craftNativeChatRefusal struct{ reason string }

func (e craftNativeChatRefusal) Error() string { return "native Craft Sheet refused: " + e.reason }

func replyChatSheetRefusal(w http.ResponseWriter, status int, reason string) {
	text := craftSheetReasonText[reason]
	if text == "" {
		text = craftSheetReasonUnavailableText
	}
	writeJSON(w, status, map[string]string{"error": text, "reason": reason})
}

type craftNativeChatRunKey struct{}

// CraftNativeChatRun reports whether an agent engine call serves an ordinary
// chat run's attached Sheet (as opposed to a native Craft task). The engine
// wiring uses it to choose a persistent session for chat runs only.
func CraftNativeChatRun(ctx context.Context) bool {
	v, _ := ctx.Value(craftNativeChatRunKey{}).(bool)
	return v
}

// craftNativeChatRunMaxAge bounds how long a chat run's registration lingers
// after the run is gone, so finished runs never fill the 128-entry registry.
const craftNativeChatRunMaxAge = 2 * time.Hour

// craftNativeChatRunEngineWindow bounds one engine operation of a chat run, a
// cold open of a large Sheet included (measured 27 to 105 s under load).
const craftNativeChatRunEngineWindow = 170 * time.Second

// craftNativeChatRunWriteWindow is how long the host keeps the connection open
// for the answer: the engine window plus the save.
const craftNativeChatRunWriteWindow = craftNativeChatRunEngineWindow + 20*time.Second

type craftNativeChatRun struct {
	authority artifactRunAuthority
	created   time.Time
	// prewarmed is set by the run's first prewarm request; later ones are no-ops.
	prewarmed atomic.Bool
	// consentVersion is the revision the person allowed the agent to change
	// (artifact_message_refs.consent_version); 0 means none was recorded.
	consentVersion int64
	// viaAgentFiles: no Sheet is attached to the message; the run reads the one Sheet the agent was
	// given (agent_files) in the person's own private chat with it. Read only, audited.
	viaAgentFiles bool
	// lastApply binds the run's next write to its own last version and identifies
	// an exact retry of that write.
	lastApply atomic.Pointer[craftNativeChatApplyKey]
}

type craftNativeChatApplyKey struct {
	requestID, base string
	version         int64
}

func (c *craftNativeChatRun) consentBase() int64 {
	if last := c.lastApply.Load(); last != nil {
		return last.version
	}
	return c.consentVersion
}

// rememberApply runs under the agent entry's call lock, on first confirmation only.
func (c *craftNativeChatRun) rememberApply(input craftNativeAgentToolRequest, receipt mooring.NativeSaveReceipt) {
	if c == nil {
		return
	}
	if version, err := craftNativeVersion(receipt.Version); err == nil {
		c.lastApply.Store(&craftNativeChatApplyKey{requestID: input.RequestID, base: input.BaseVersion, version: version})
	}
}

var errCraftNativeConsentStale = errors.New("stale Sheet write consent")

// chatRunTool serves one Sheet tool call of a chat run.
func (m *craftNativeAgentManager) chatRunTool(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if m == nil || !m.enabled || m.config.AgentApply == nil || m.config.AgentInspect == nil || m.human == nil || m.human.pages == nil {
		replyError(w, 503, "native Craft agents unavailable")
		return
	}
	gate := &craftNativeChatBuffer{header: http.Header{}}
	r, ok := m.human.pages.artifactAgentRequestMode(gate, r, false)
	if !ok {
		m.replyChatRunGate(w, r, gate)
		return
	}
	a, _ := r.Context().Value(artifactRunKey{}).(*artifactRunAuthority)
	if a == nil || a.Unattended || a.HumanUserID == "" {
		logChatRunRefusal(r.Context(), "authority", 403, nil, nil)
		replyChatSheetRefusal(w, 403, CraftSheetReasonNotAttached)
		return
	}
	// The first call of a run may wait for the engine to open a large Sheet
	// for well over a minute; the host's write deadline (30 s on the internal
	// listener) must not cut the answer off. Best effort: a writer that cannot
	// extend its deadline keeps the listener's own.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(craftNativeChatRunWriteWindow))
	bound, e, err := m.bindChatRun(r, a)
	if err != nil {
		var refusal craftNativeChatRefusal
		reason := CraftSheetReasonNotAttached
		if errors.As(err, &refusal) {
			reason = refusal.reason
		} else {
			logChatRunRefusal(r.Context(), "binding", 403, nil, err)
		}
		replyChatSheetRefusal(w, 403, reason)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
	if err != nil {
		replyError(w, 413, "native Craft request too large")
		return
	}
	if craftNativeChatPrewarmRequest(raw) {
		m.prewarmChatRun(w, bound, e)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	bound.Body = r.Body
	input, parseErr := parseCraftNativeAgentTool(raw)
	// A Sheet known only from agent_files is read, never changed: a write needs the chip.
	if e.chat.viaAgentFiles && (parseErr != nil || input.Operation != "read") {
		replyChatSheetRefusal(w, 403, CraftSheetReasonNotAttached)
		return
	}
	// A write names the consented revision (then the run's own), and the Sheet
	// is still at it: anything else in between means the person's consent no
	// longer covers what would be changed.
	if parseErr == nil && input.Operation == "apply" {
		if stale, err := m.chatRunConsentStale(r.Context(), e, input.BaseVersion, input.RequestID); err != nil {
			replyChatSheetRefusal(w, 503, CraftSheetReasonEngineFailed)
			return
		} else if stale {
			replyChatSheetRefusal(w, http.StatusConflict, CraftSheetReasonConsentStale)
			return
		}
	}
	buffered := &craftNativeChatBuffer{header: http.Header{}}
	m.serveToolCall(buffered, r, bound, e)
	if e.chat.viaAgentFiles && buffered.status == http.StatusOK {
		m.auditChatRunAgentFileRead(r.Context(), e)
	}
	buffered.finish(w, m, e, input, parseErr == nil)
}

// replyChatRunGate passes the run gate's refusal on. A valid run token whose run
// was admitted without a human (the workspace flag was off when it started, or
// nothing was attached) is told why, as every other refusal is; a token that did
// not prove a run keeps the gate's own answer.
func (m *craftNativeAgentManager) replyChatRunGate(w http.ResponseWriter, r *http.Request, gate *craftNativeChatBuffer) {
	var answer struct {
		Error string `json:"error"`
	}
	if gate.status == http.StatusForbidden && json.Unmarshal(gate.body.Bytes(), &answer) == nil && answer.Error == "no_human_principal" {
		reason := CraftSheetReasonNotAttached
		if !m.human.pages.artifactRunModesOn(r.Context(), InternalTokenWorkspaceFromContext(r.Context())) {
			reason = CraftSheetReasonFeatureOff
		}
		replyChatSheetRefusal(w, http.StatusForbidden, reason)
		return
	}
	for k, v := range gate.header {
		w.Header()[k] = v
	}
	w.WriteHeader(gate.status)
	_, _ = w.Write(gate.body.Bytes())
}

// craftNativeChatApplyResult is what a successful apply tells a chat run and
// its card: the Sheet it changed, the new version and how many cells the
// request wrote. The receipt stays for the agent's own reconciliation.
type craftNativeChatApplyResult struct {
	PageID       string `json:"page_id"`
	Slug         string `json:"slug"`
	Version      int64  `json:"version"`
	ChangedCells int    `json:"changed_cells"`
	// OperationID is the request ID the change was saved under (the operations log and undo).
	OperationID string `json:"operation_id,omitempty"`
	// Summary says where the change wrote, for the card.
	Summary craftNativeChangeSummary `json:"summary"`
	// Undoable: the server kept what the change replaced, so craft/document/undo can put it back.
	Undoable bool `json:"undoable"`
	// SignatureInvalidated: the workbook's XML package signature was kept but no longer matches.
	SignatureInvalidated bool `json:"signature_invalidated,omitempty"`
	// BaseVersion is the version the change was made on; Version is BaseVersion+1
	// for a change that created one.
	BaseVersion int64 `json:"base_version"`
	// ContentHash is "sha256:" and the hex SHA-256 of exactly the bytes of the
	// stored version, the bytes a read of the document returns.
	ContentHash string `json:"content_hash"`
	// Commands are the commands the server accepted and applied, as sent. They
	// are left out (CommandsOmitted) when they would not fit the tool result, and
	// whoever shows the change then reopens the document instead.
	Commands        json.RawMessage `json:"commands,omitempty"`
	CommandsOmitted bool            `json:"commands_omitted,omitempty"`
	Receipt         json.RawMessage `json:"receipt"`
}

// craftNativeChatCommandsLimit is the most command bytes the result carries.
// A request is at most 128 KiB, so today every accepted request fits.
const craftNativeChatCommandsLimit = 256 << 10

// craftNativeChatBuffer holds the tool's answer so a successful apply can be
// given its typed shape, and so a failure carries a short code and no more.
type craftNativeChatBuffer struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *craftNativeChatBuffer) Header() http.Header { return b.header }
func (b *craftNativeChatBuffer) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}
func (b *craftNativeChatBuffer) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

func (b *craftNativeChatBuffer) finish(w http.ResponseWriter, m *craftNativeAgentManager, e *craftNativeAgentEntry, input craftNativeAgentToolRequest, parsed bool) {
	for k, v := range b.header {
		w.Header()[k] = v
	}
	status := b.status
	if status == 0 {
		status = http.StatusOK
	}
	if status == http.StatusOK && parsed && input.Operation == "apply" {
		var receipt mooring.NativeSaveReceipt
		version, versionErr := strconv.ParseInt(receipt.Version, 10, 64)
		if json.Unmarshal(b.body.Bytes(), &receipt) == nil {
			version, versionErr = strconv.ParseInt(receipt.Version, 10, 64)
		}
		var slug string
		_ = m.human.pages.db.QueryRow(`SELECT slug FROM pages WHERE id=? AND workspace_id=?`, e.grant.Scope.ArtifactID, e.grant.Scope.WorkspaceID).Scan(&slug)
		if versionErr == nil && version > 0 && slug != "" {
			result := craftNativeChatApplyResult{PageID: e.grant.Scope.ArtifactID, Slug: slug, Version: version, ChangedCells: nativeAgentChangedCells(input.Commands), ContentHash: receipt.ContentHash, Receipt: json.RawMessage(b.body.Bytes())}
			result.BaseVersion, _ = strconv.ParseInt(receipt.BaseVersion, 10, 64)
			result.OperationID, result.Summary = nativeAgentOperationID(e, input.RequestID), nativeAgentChangeSummary(input.Commands)
			e.callMu.Lock()
			if e.tool != nil {
				if attempt := e.tool.requests[input.RequestID]; attempt != nil {
					result.Undoable = attempt.undoable
					result.SignatureInvalidated = attempt.signatureInvalidated
				}
			}
			e.callMu.Unlock()
			var compact bytes.Buffer
			if len(input.Commands) <= craftNativeChatCommandsLimit && json.Compact(&compact, input.Commands) == nil {
				result.Commands = json.RawMessage(compact.Bytes())
			} else {
				result.CommandsOmitted = true
			}
			writeJSON(w, http.StatusOK, result)
			return
		}
	}
	if status != http.StatusOK {
		if status == http.StatusForbidden {
			logChatRunRefusal(context.Background(), "tool", status, b.body.Bytes(), nil)
		}
		reason := craftNativeChatReason(status, b.body.Bytes(), input, parsed)
		if craftNativeChatPassThrough(w, status, reason, b.body.Bytes()) {
			return
		}
		replyChatSheetRefusal(w, status, reason)
		return
	}
	w.WriteHeader(status)
	_, _ = w.Write(b.body.Bytes())
}

// craftNativeChatReason names a failed tool call from its status and the
// tool's own answer, never from engine text or document content.
func craftNativeChatReason(status int, body []byte, input craftNativeAgentToolRequest, parsed bool) string {
	var answer struct {
		Reason     string `json:"reason"`
		Code       string `json:"code"`
		HolderKind string `json:"holder_kind"`
	}
	_ = json.Unmarshal(body, &answer)
	switch {
	case status == http.StatusServiceUnavailable && answer.Reason == CraftSheetReasonBusy:
		return CraftSheetReasonBusy
	case status == http.StatusForbidden:
		return CraftSheetReasonNotAttached
	case status == http.StatusConflict && answer.HolderKind == "human":
		return CraftSheetReasonHolderHuman
	case status == http.StatusConflict && answer.Reason == "proposal_required":
		return CraftSheetReasonProposal
	case status == http.StatusConflict && (answer.Reason == CraftSheetReasonConsentStale || answer.Reason == CraftSheetReasonNotUndoable):
		return answer.Reason
	case status == http.StatusConflict && answer.Code == CraftSheetReasonLeaseExpired:
		return CraftSheetReasonLeaseExpired
	case status == http.StatusConflict:
		return CraftSheetReasonStale
	case status == http.StatusUnprocessableEntity && answer.Reason == CraftSheetReasonFidelityLoss:
		return CraftSheetReasonFidelityLoss
	case status == http.StatusRequestEntityTooLarge:
		return CraftSheetReasonTooLarge
	case status == http.StatusBadRequest && (answer.Reason == CraftSheetReasonFormula || parsed && nativeAgentFormulaLike(input.Commands)):
		return CraftSheetReasonFormula
	case status == http.StatusBadRequest:
		return CraftSheetReasonInvalid
	}
	return CraftSheetReasonEngineFailed
}

// nativeAgentFormulaLike reports whether a request writes a string the engine
// would read as a formula. It looks at the shape only, to name a refusal.
func nativeAgentFormulaLike(commands json.RawMessage) bool {
	var entries []struct {
		Params struct {
			Input  string              `json:"input"`
			Values [][]json.RawMessage `json:"values"`
		} `json:"params"`
	}
	if json.Unmarshal(commands, &entries) != nil {
		return false
	}
	for _, entry := range entries {
		if nativeAgentFormulaText(entry.Params.Input) {
			return true
		}
		for _, row := range entry.Params.Values {
			for _, cell := range row {
				var text string
				if json.Unmarshal(cell, &text) == nil && nativeAgentFormulaText(text) {
					return true
				}
			}
		}
	}
	return false
}

// nativeAgentChangedCells counts the cells an apply request writes: one per
// cell.set, rows times columns per range.setValues. It reads no cell content.
func nativeAgentChangedCells(commands json.RawMessage) int {
	return nativeAgentChangeSummary(commands).CellsChanged
}

func (m *craftNativeAgentManager) bindChatRun(r *http.Request, a *artifactRunAuthority) (*http.Request, *craftNativeAgentEntry, error) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return nil, nil, chatRunDenied(1)
	}
	hash := sha256.Sum256([]byte(parts[1]))
	m.mu.Lock()
	e := m.runs[a.RunID]
	m.mu.Unlock()
	if e == nil {
		var err error
		if e, err = m.attachChatRunEntry(r.Context(), a, hash); err != nil {
			return nil, nil, err
		}
	}
	m.mu.Lock()
	valid := e.chat != nil && !e.closed && e.bearerHash == hash && e.grant.AgentID == a.AgentID && e.grant.CrewID == a.CrewID && e.grant.Scope.WorkspaceID == a.WorkspaceID && e.chat.authority.MessageID == a.MessageID
	m.mu.Unlock()
	if !valid {
		return nil, nil, chatRunDenied(2)
	}
	c := &craftNativeAgentCall{m, e, hash}
	ctx := context.WithValue(context.WithValue(r.Context(), craftNativeAgentContextKey{}, c), craftNativeChatRunKey{}, true)
	return r.WithContext(ctx), e, nil
}

// attachChatRunEntry registers the one attached Sheet of a chat run, once, in
// the same writer transaction that checks its reach. It refuses anything but
// exactly one attached native Sheet that the run's human owns personally.
func (m *craftNativeAgentManager) attachChatRunEntry(ctx context.Context, a *artifactRunAuthority, hash [sha256.Size]byte) (*craftNativeAgentEntry, error) {
	if m == nil || !m.enabled || m.human == nil || m.human.clock == nil || m.documents == nil || a == nil ||
		!mooring.ValidWireIdentifier(a.RunID) || !mooring.ValidWireIdentifier(a.AgentID) || !mooring.ValidWireIdentifier(a.CrewID) || !mooring.ValidWireIdentifier(a.ChatID) || !mooring.ValidWireIdentifier(a.MessageID) {
		return nil, chatRunDenied(3)
	}
	tx, err := m.writer(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	pages := m.human.pages
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	// With the workspace flag off nothing here exists: the run has no Sheet.
	if !pages.artifactRunModesOn(ctx, a.WorkspaceID) {
		return nil, craftNativeChatRefusal{CraftSheetReasonFeatureOff}
	}
	origin, err := runauthority.Resolve(ctx, tx, a.RunID, a.AgentID, a.WorkspaceID)
	if err != nil || origin.HumanAuthority == nil || origin.ChatID != a.ChatID || origin.TriggerMessageID != a.MessageID {
		return nil, chatRunDenied(4)
	}
	source := origin.HumanAuthority.OriginalCredential()
	if source.UserID != a.HumanUserID {
		return nil, chatRunDenied(5)
	}
	rows, err := tx.QueryContext(ctx, `SELECT page_id FROM artifact_run_scope WHERE run_id=? AND workspace_id=? ORDER BY page_id`, a.RunID, a.WorkspaceID)
	if err != nil {
		return nil, chatRunDenied(6)
	}
	var attached []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return nil, chatRunDenied(7)
		}
		attached = append(attached, id)
	}
	if rows.Close() != nil || rows.Err() != nil {
		return nil, chatRunDenied(8)
	}
	var (
		rec   *pageRecord
		proof craftNativeProof
		scope mooring.EditLeaseScope
	)
	skipped := 0 // 1 not loaded, 2 no write authority, 3 not a Sheet (the last one seen)
	for _, id := range attached {
		candidate, loadErr := pages.loadPageForRead(ctx, a.WorkspaceID, "@"+id)
		if loadErr != nil {
			skipped = 1
			continue
		}
		s := mooring.EditLeaseScope{WorkspaceID: a.WorkspaceID, ArtifactID: candidate.ID}
		p, authErr := m.human.pageAuthority(ctx, tx, source, s, true)
		if authErr != nil {
			skipped = 2
			continue
		}
		if p.application != "gridcraft" {
			skipped = 3
			continue
		}
		if rec != nil {
			return nil, chatRunDenied(9) // more than one attached Sheet: the tools would have to choose
		}
		rec, proof, scope = candidate, p, s
	}
	// Nothing attached: the one Sheet the agent was given, read only, when the person still may and
	// this is their own private chat with the agent (agent_files grants nothing by itself).
	viaFiles := false
	if rec == nil && len(attached) == 0 {
		if id, ok := chatRunAgentFile(ctx, tx, a); ok {
			if candidate, loadErr := pages.loadPageForRead(ctx, a.WorkspaceID, "@"+id); loadErr == nil {
				s := mooring.EditLeaseScope{WorkspaceID: a.WorkspaceID, ArtifactID: candidate.ID}
				if p, authErr := m.human.pageAuthority(ctx, tx, source, s, true); authErr == nil && p.application == "gridcraft" {
					rec, proof, scope, viaFiles = candidate, p, s, true
				}
			}
		}
	}
	if !pages.artifactAgentPolicyAllows(ctx, a.WorkspaceID, a.CrewID) {
		return nil, craftNativeChatRefusal{CraftSheetReasonAgentPolicy}
	}
	if rec == nil {
		return nil, chatRunDenied(100 + skipped)
	}
	if rec.OwnerUserID != a.HumanUserID || rec.OwnerCrewID != "" || !craftNativePersonalSpace(rec, a.HumanUserID) {
		return nil, chatRunDenied(10)
	}
	var consent sql.NullInt64
	if !viaFiles {
		if reach, _, reachErr := pages.artifactRunReach(ctx, a, rec); reachErr != nil || reach != runReachReferenced {
			return nil, chatRunDenied(11)
		}
		if _, err = pages.authorizeScopedArtifactRun(ctx, tx, a, a.WorkspaceID, rec, true, false); err != nil {
			return nil, chatRunDenied(12)
		}
		if err = tx.QueryRowContext(ctx, `SELECT consent_version FROM artifact_message_refs WHERE message_id=? AND page_id=? AND workspace_id=? AND human_user_id=?`, a.MessageID, rec.ID, a.WorkspaceID, a.HumanUserID).Scan(&consent); err != nil {
			return nil, chatRunDenied(17)
		}
	}
	var instruction string
	if err = tx.QueryRowContext(ctx, `SELECT content FROM conversation_messages WHERE id=? AND session_id=? AND author_user_id=? AND role='user' AND length(CAST(content AS BLOB))<=?`,
		a.MessageID, a.ChatID, a.HumanUserID, mooring.MaxRPCFrame).Scan(&instruction); err != nil {
		return nil, chatRunDenied(13)
	}
	boot, now, err := m.human.clock.Now()
	if err != nil || boot == "" || now < 0 {
		return nil, chatRunDenied(14)
	}
	ttl := time.Hour
	if !proof.expiry.IsZero() && time.Until(proof.expiry) < ttl {
		ttl = time.Until(proof.expiry)
	}
	if ttl <= 0 || now > time.Duration(math.MaxInt64)-ttl {
		return nil, chatRunDenied(15)
	}
	g := craftNativeAgentGrant{Scope: scope, Application: proof.application, Source: source, Original: *origin.HumanAuthority, MessageID: a.MessageID, AgentID: a.AgentID, CrewID: a.CrewID, Proof: proof, Boot: boot, Deadline: now + ttl, Instruction: instruction}
	g.Mode, g.ModeExplicit = craftNativeAgentMode("", true)
	if g.Epochs, err = craftNativeAgentEpochs(ctx, tx, g, true); err != nil {
		return nil, err
	}
	if err = m.checkOriginalGrant(ctx, tx, g); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(a.RunID + "|" + rec.ID))
	e := &craftNativeAgentEntry{grant: g, workID: craftNativeChatRunPrefix + a.RunID, inputSHA256: hex.EncodeToString(sum[:]), id: a.RunID, seal: m.seal, bearerHash: hash,
		chat: &craftNativeChatRun{authority: *a, created: time.Now(), consentVersion: consent.Int64, viaAgentFiles: viaFiles}}
	e.attempt.runID = a.RunID
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneChatRunsLocked()
	if m.runs[a.RunID] != nil {
		return m.runs[a.RunID], nil
	}
	if len(m.entries) >= 128 || m.ctx.Err() != nil {
		return nil, chatRunDenied(16)
	}
	m.entries[e.workID] = e
	m.runs[a.RunID] = e
	return e, nil
}

func (m *craftNativeAgentManager) pruneChatRunsLocked() {
	for id, e := range m.entries {
		if e.chat != nil && (e.closed || time.Since(e.chat.created) > craftNativeChatRunMaxAge) {
			e.closed = true
			delete(m.entries, id)
			delete(m.runs, e.attempt.runID)
		}
	}
}

// checkChatRun is checkRun for a chat run's entry. Everything is re-derived
// from the database: the captured grant (original human, epochs, policy, owner
// still allowing this agent), the run's own principal and origin, and the
// run scope's reach and ownership for a write. A failed check ends the access.
func (m *craftNativeAgentManager) checkChatRun(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry) error {
	if err := m.checkGrant(ctx, tx, e); err != nil {
		return err
	}
	if e.chat == nil || e.attempt.runID == "" {
		return errCraftNativeAgentDenied
	}
	a := e.chat.authority
	g := e.grant
	origin, err := runauthority.Resolve(ctx, tx, a.RunID, g.AgentID, g.Scope.WorkspaceID)
	if err != nil || origin.HumanAuthority == nil || *origin.HumanAuthority != g.Original || origin.ChatID != g.Original.ChatID || origin.TriggerMessageID != g.MessageID {
		return errCraftNativeAgentDenied
	}
	pages := m.human.pages
	ctx = context.WithValue(ctx, pageAuthReaderKey{}, tx)
	// Turning the flag off ends every chat run's access at its next call.
	if !pages.artifactRunModesOn(ctx, g.Scope.WorkspaceID) {
		return errCraftNativeAgentDenied
	}
	rec, err := pages.loadPageForRead(ctx, g.Scope.WorkspaceID, "@"+g.Scope.ArtifactID)
	if err != nil || rec.OwnerUserID != g.Source.UserID || rec.OwnerCrewID != "" {
		return errCraftNativeAgentDenied
	}
	if e.chat.viaAgentFiles {
		if id, ok := chatRunAgentFile(ctx, tx, &a); !ok || id != g.Scope.ArtifactID {
			return errCraftNativeAgentDenied
		}
		return nil
	}
	if reach, _, reachErr := pages.artifactRunReach(ctx, &a, rec); reachErr != nil || reach != runReachReferenced {
		return errCraftNativeAgentDenied
	}
	if _, err = pages.authorizeScopedArtifactRun(ctx, tx, &a, g.Scope.WorkspaceID, rec, true, false); err != nil {
		return errCraftNativeAgentDenied
	}
	return nil
}

// chatRunAgentFile is the one Sheet the run's agent was given (agent_files, not taken back) when the
// run's chat is its person's own private chat with this agent; not ok for none or several.
func chatRunAgentFile(ctx context.Context, tx *sql.Tx, a *artifactRunAuthority) (string, bool) {
	rows, err := tx.QueryContext(ctx, `SELECT f.page_id FROM agent_files f
 JOIN chats c ON c.id=? AND c.workspace_id=f.workspace_id AND c.created_by=? AND c.agent_id=f.agent_id AND c.visibility='private'
 JOIN pages p ON p.id=f.page_id AND p.workspace_id=f.workspace_id AND p.archived_at IS NULL
 WHERE f.workspace_id=? AND f.agent_id=? AND f.removed_at IS NULL LIMIT 2`, a.ChatID, a.HumanUserID, a.WorkspaceID, a.AgentID)
	if err != nil {
		return "", false
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			return "", false
		}
		ids = append(ids, id)
	}
	if rows.Err() != nil || len(ids) != 1 {
		return "", false
	}
	return ids[0], true
}

// auditChatRunAgentFileRead records a read of an agent file without the chip (source agent_files).
// It carries no cell content.
func (m *craftNativeAgentManager) auditChatRunAgentFileRead(ctx context.Context, e *craftNativeAgentEntry) {
	h := m.human.pages
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		slog.WarnContext(ctx, "craft agent file read audit", "error", err)
		return
	}
	defer tx.Rollback()
	notify, err := appendAuthorizationAudit(ctx, tx, h.journal, journal.Entry{
		WorkspaceID: e.grant.Scope.WorkspaceID,
		Type:        journal.EntryArtifactSheetRead,
		ActorType:   journal.ActorUser,
		ActorID:     e.grant.Source.UserID,
		AgentID:     e.grant.AgentID,
		Summary:     "Agent read an attached Sheet",
		Payload: map[string]any{
			"source": "agent_files", "agent_id": e.grant.AgentID, "run_id": e.chat.authority.RunID,
			"page_id": e.grant.Scope.ArtifactID,
		},
	})
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		slog.WarnContext(ctx, "craft agent file read audit", "error", err)
		return
	}
	notify()
}

// craftNativeChatPrewarmRequest recognises the one request that is not a Sheet
// operation: {"operation":"prewarm"} and nothing else.
func craftNativeChatPrewarmRequest(raw []byte) bool {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) != nil || len(body) != 1 {
		return false
	}
	var op string
	return json.Unmarshal(body["operation"], &op) == nil && op == "prewarm"
}

// craftNativeChatPrewarmEvery is how often one conversation may start a
// prewarm: the engine keeps a session for ten minutes, a second prewarm in that
// time would only hold a second copy.
const craftNativeChatPrewarmEvery = 10 * time.Minute

// prewarmChatRun opens the attached Sheet in the engine in the background so
// the run's first real call does not wait for it. The request has passed the
// same binding as every Sheet call (attached, owned, flag, policy, live human).
// It is a hint: it answers 202 at once, reads and writes nothing, holds no
// lease and charges nothing. It runs once per run and once per conversation per
// ten minutes, and the open is dropped if the authority is gone before it starts.
func (m *craftNativeAgentManager) prewarmChatRun(w http.ResponseWriter, bound *http.Request, e *craftNativeAgentEntry) {
	if err := m.toolAuthority(bound.Context(), e); err != nil {
		replyChatSheetRefusal(w, 403, CraftSheetReasonNotAttached)
		return
	}
	if !e.chat.prewarmed.CompareAndSwap(false, true) || !m.reservePrewarm(e.chat.authority.ChatID) {
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "skipped"})
		return
	}
	application, ok := m.documents.application(e.grant.Application)
	profile, pinned := m.documents.profile(e.grant.Application)
	if !ok || !pinned {
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "skipped"})
		return
	}
	cfg := application.Store
	cfg.Enabled = m.human.enabled
	cfg.Leases = m.human.leases
	cfg.Authorize = m.human.authorizeNative
	state, err := mooring.NewNativeStore(m.human.pages.db, cfg).Read(bound.Context(), e.grant.Scope)
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "skipped"})
		return
	}
	native, err := nativeAgentDocumentBytes(state)
	if err != nil {
		writeJSON(w, http.StatusAccepted, map[string]string{"state": "skipped"})
		return
	}
	// The request's identity without its cancellation: the answer goes out at
	// once, the open goes on.
	detached := context.WithoutCancel(bound.Context())
	finish := beginBackgroundWork()
	go func() {
		defer finish()
		ctx, cancel := context.WithTimeout(detached, craftNativeChatRunEngineWindow)
		defer cancel()
		// Dropped if the person, the flag or the attachment changed meanwhile.
		if m.toolAuthority(ctx, e) != nil {
			return
		}
		_, _ = m.config.AgentInspect(ctx, profile, native, json.RawMessage(`{"command":"document.inspect","params":{}}`))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "started"})
}

// reservePrewarm admits one prewarm per conversation per craftNativeChatPrewarmEvery.
func (m *craftNativeAgentManager) reservePrewarm(chatID string) bool {
	m.prewarmMu.Lock()
	defer m.prewarmMu.Unlock()
	now := time.Now()
	for id, at := range m.prewarmChats {
		if now.Sub(at) > craftNativeChatPrewarmEvery {
			delete(m.prewarmChats, id)
		}
	}
	if _, busy := m.prewarmChats[chatID]; busy {
		return false
	}
	if m.prewarmChats == nil {
		m.prewarmChats = map[string]time.Time{}
	}
	m.prewarmChats[chatID] = now
	return true
}

// ChatRunReadiness says whether an agent in an ordinary chat can read and change
// an attached Sheet right now, and the reason when it cannot. It looks at what
// the path needs: the agent adapter, the engine adapters and a pinned Sheet
// engine. The per-run checks (flag, policy, attachment) are not a property of the
// server and are not part of it.
func (m *craftNativeAgentManager) ChatRunReadiness() (craftapps.State, craftapps.Reason) {
	switch {
	case m == nil || m.human == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentRuntimeUnavailable
	case !m.enabled:
		return craftapps.StateDisabled, craftapps.ReasonAppDisabled
	case m.config.AgentApply == nil || m.config.AgentInspect == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentEngineMissing
	}
	if _, pinned := m.documents.profile("gridcraft"); !pinned {
		return craftapps.StateUnavailable, craftapps.ReasonSheetEngineNotPinned
	}
	return craftapps.StateReady, craftapps.ReasonNone
}

// TasksReadiness is the same for a background document task, which also needs
// the runtime that launches it and its dispatcher.
func (m *craftNativeAgentManager) TasksReadiness() (craftapps.State, craftapps.Reason) {
	switch {
	case m == nil || m.human == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentRuntimeUnavailable
	case !m.enabled:
		return craftapps.StateDisabled, craftapps.ReasonAppDisabled
	case m.runtime == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentRuntimeUnavailable
	case m.dispatcher == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentDispatcherMissing
	case m.config.AgentApply == nil || m.config.AgentInspect == nil:
		return craftapps.StateUnavailable, craftapps.ReasonAgentEngineMissing
	}
	return craftapps.StateReady, craftapps.ReasonNone
}

// craftNativeChatRefusalCode keeps only a short machine code from a refusal
// body, so the log never carries a document's text.
var craftNativeChatRefusalCode = regexp.MustCompile(`^[A-Za-z0-9 _.:-]{1,64}$`)

// logChatRunRefusal says which check turned a chat run's Sheet call into
// not_attached: the agent and the person see one reason, the operator the stage.
func logChatRunRefusal(ctx context.Context, stage string, status int, body []byte, err error) {
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &answer)
	code := answer.Error
	if !craftNativeChatRefusalCode.MatchString(code) {
		code = ""
	}
	cause := ""
	if err != nil && craftNativeChatRefusalCode.MatchString(err.Error()) {
		cause = err.Error()
	}
	slog.WarnContext(ctx, "craft chat run sheet call refused", "stage", stage, "status", status, "code", code, "cause", cause)
}

// chatRunDenied marks which binding check refused, for the operator's log
// only: callers still see errCraftNativeAgentDenied and the agent not_attached.
func chatRunDenied(check int) error {
	return fmt.Errorf("%w: chat run check %d", errCraftNativeAgentDenied, check)
}

// chatRunConsentStale checks whether the Sheet has moved since the person
// consented or the run last wrote it. After binding and checking the lease,
// applyTool requires the request to name that same base and saves with a CAS.
func (m *craftNativeAgentManager) chatRunConsentStale(ctx context.Context, e *craftNativeAgentEntry, base, requestID string) (bool, error) {
	if e == nil || e.chat == nil {
		return true, nil
	}
	if last := e.chat.lastApply.Load(); last != nil && last.requestID == requestID && last.base == base {
		return false, nil
	}
	want := e.chat.consentBase()
	if want <= 0 {
		return true, nil
	}
	var head int64
	err := m.human.pages.db.QueryRowContext(ctx, `SELECT latest_version FROM artifact_version_heads WHERE workspace_id=? AND page_id=?`, e.grant.Scope.WorkspaceID, e.grant.Scope.ArtifactID).Scan(&head)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return head != want, nil
}

type craftNativeChangeSummary struct {
	CellsChanged int      `json:"cells_changed"`
	Sheets       []string `json:"sheets"`
	Ranges       []string `json:"ranges"`
	Truncated    bool     `json:"truncated,omitempty"`
}

// craftNativeSummaryRanges is the most ranges a change summary lists.
const craftNativeSummaryRanges = 20

// nativeAgentChangeSummary says where an agent change wrote: the sheets in the order first
// written, each range (its sheet in the address) and how many cells it wrote or cleared.
func nativeAgentChangeSummary(commands json.RawMessage) craftNativeChangeSummary {
	var entries []struct {
		Command string          `json:"command"`
		Params  json.RawMessage `json:"params"`
	}
	out := craftNativeChangeSummary{Sheets: []string{}, Ranges: []string{}}
	if json.Unmarshal(commands, &entries) != nil {
		return out
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		key := "range"
		if entry.Command == "cell.set" {
			key = "cell"
		}
		lifted, ok := nativeAgentLiftSheet(entry.Command, entry.Params)
		var params map[string]json.RawMessage
		if !ok || json.Unmarshal(lifted, &params) != nil {
			continue
		}
		cells := 0
		switch entry.Command {
		case "cell.set":
			cells = 1
		case "range.setValues":
			var values [][]json.RawMessage
			_ = json.Unmarshal(params["values"], &values)
			for _, row := range values {
				cells += len(row)
			}
		case "range.clear":
			if area, ok := nativeAgentEngineRange(params["range"]); ok {
				cells = area.cells()
			}
		default:
			continue
		}
		address, err := nativeAgentSheetAddress("", params, key)
		if err != nil {
			continue
		}
		out.CellsChanged += cells
		var sheet string
		if raw, ok := params["sheet"]; ok && json.Unmarshal(raw, &sheet) == nil && !seen[sheet] {
			seen[sheet] = true
			out.Sheets = append(out.Sheets, sheet)
		}
		if len(out.Ranges) < craftNativeSummaryRanges {
			out.Ranges = append(out.Ranges, address)
		} else {
			out.Truncated = true
		}
	}
	return out
}

// craftNativeChatPassThrough keeps a refusal that tells the agent what to fix: a write without its
// sheet (invalid_request with the instruction) and a fidelity loss with its parts.
func craftNativeChatPassThrough(w http.ResponseWriter, status int, reason string, body []byte) bool {
	var answer struct {
		Error  string          `json:"error"`
		Reason string          `json:"reason"`
		Parts  json.RawMessage `json:"parts"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return false
	}
	switch {
	case reason == CraftSheetReasonFidelityLoss && len(answer.Parts) > 0:
		writeJSON(w, status, map[string]any{"error": answer.Error, "reason": reason, "parts": answer.Parts})
	case reason == CraftSheetReasonInvalid && answer.Error == errCraftNativeSheetNameRequired.Error():
		writeJSON(w, status, map[string]string{"error": answer.Error, "reason": reason})
	default:
		return false
	}
	return true
}
