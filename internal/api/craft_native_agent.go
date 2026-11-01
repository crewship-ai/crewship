package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// These callbacks are operator-owned integrations with the qualified native
// engine. A request never supplies an executable, profile or validator.
type CraftNativeAgentApply func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) ([]byte, error)
type CraftNativeAgentInspect func(context.Context, CraftNativeDocumentProfile, []byte, json.RawMessage) (json.RawMessage, error)

var errCraftNativeAgentDenied = errors.New("native Craft agent mandate denied")

const craftNativeAgentDomain = "craft_native_task"

// The whole-document scope is explicit, never inferred from instruction text.
// The chat supplies the actual registered agent; callers cannot nominate a run,
// principal, generation, credential, executable or previously queued work.
type craftNativeAgentTaskRequest struct {
	ChatID        string `json:"chat_id"`
	MessageID     string `json:"message_id"`
	WholeDocument bool   `json:"whole_document"`
}

func parseCraftNativeAgentTask(raw []byte) (craftNativeAgentTaskRequest, error) {
	var input craftNativeAgentTaskRequest
	canonical, err := mooring.CanonicalJSON(raw)
	var fields map[string]json.RawMessage
	if err != nil || json.Unmarshal(canonical, &fields) != nil || len(fields) != 3 {
		return input, errCraftNativeAgentDenied
	}
	for _, name := range []string{"chat_id", "message_id", "whole_document"} {
		if fields[name] == nil {
			return input, errCraftNativeAgentDenied
		}
	}
	if json.Unmarshal(canonical, &input) != nil || string(fields["whole_document"]) != "true" || !mooring.ValidWireIdentifier(input.ChatID) || !mooring.ValidWireIdentifier(input.MessageID) {
		return input, errCraftNativeAgentDenied
	}
	return input, nil
}

// One original, immutable human grant. SQL work/input binds this exact value to
// dispatch, but neither SQL nor a bearer can reconstruct this process's pending
// consent. The registry's private entry must also survive through confirmation.
type craftNativeAgentGrant struct {
	Scope       mooring.EditLeaseScope
	Application string
	Source      auth.OriginalCredential
	Original    chataudience.Receipt
	MessageID   string
	AgentID     string
	CrewID      string
	Proof       craftNativeProof
	Epochs      [4]string
	Boot        string
	Deadline    time.Duration
	Instruction string
}

type craftNativeAgentEntry struct {
	tool              *craftNativeAgentToolState
	callMu            sync.Mutex
	grant             craftNativeAgentGrant
	workID            string
	inputSHA256       string
	id                string
	seal              *byte
	attempt           collaborationAgentAttemptBinding
	bearerHash        [sha256.Size]byte
	closed            bool
	claimedRun        string
	claimedAttempt    int
	claimedGeneration int64
}

type craftNativeAgentManager struct {
	mu         sync.Mutex
	human      *craftNativeHTTP
	documents  *craftNativeDocumentsHTTP
	enabled    bool
	seal       *byte
	entries    map[string]*craftNativeAgentEntry
	runs       map[string]*craftNativeAgentEntry
	config     CraftNativeHTTPConfig
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	runtime    *craftNativeAgentRuntime
	dispatcher *dispatch.Dispatcher
}

func newCraftNativeAgentManager(h *craftNativeHTTP, d *craftNativeDocumentsHTTP, cfg CraftNativeHTTPConfig) *craftNativeAgentManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &craftNativeAgentManager{human: h, documents: d, enabled: cfg.AgentsEnabled, seal: new(byte), entries: make(map[string]*craftNativeAgentEntry), runs: make(map[string]*craftNativeAgentEntry), config: cfg, ctx: ctx, cancel: cancel}
	if h != nil {
		h.agents = m
	}
	return m
}

// This is deliberately a separate trusted path. craftNativeHuman and bound()
// remain human-only; an agent never receives or borrows a human admission.
type craftNativeAgentContextKey struct{}

func (m *craftNativeAgentManager) checkGrant(ctx context.Context, tx *sql.Tx, e *craftNativeAgentEntry) error {
	if m == nil || !m.enabled || m.ctx.Err() != nil || m.human == nil || tx == nil || e == nil || e.seal != m.seal {
		return errCraftNativeAgentDenied
	}
	m.mu.Lock()
	live := m.entries[e.workID] == e && !e.closed
	m.mu.Unlock()
	if !live {
		return errCraftNativeAgentDenied
	}
	return m.checkOriginalGrant(ctx, tx, e.grant)
}

func (m *craftNativeAgentManager) checkOriginalGrant(ctx context.Context, tx *sql.Tx, g craftNativeAgentGrant) error {
	if m == nil || m.human == nil || m.human.clock == nil || tx == nil {
		return errCraftNativeAgentDenied
	}
	boot, now, err := m.human.clock.Now()
	if err != nil || boot != g.Boot || now < 0 || now >= g.Deadline || g.Source != g.Original.OriginalCredential() {
		return errCraftNativeAgentDenied
	}
	current, err := chataudience.RevalidateOriginalUserQuery(ctx, tx, &g.Original)
	if err != nil || current == nil || *current != g.Original {
		return errCraftNativeAgentDenied
	}
	proof, err := m.human.pageAuthority(ctx, tx, g.Source, g.Scope, true)
	if err != nil || proof.application != g.Application || proof.workspaceEpoch != g.Proof.workspaceEpoch || proof.memberID != g.Proof.memberID || proof.memberRevision != g.Proof.memberRevision {
		return errCraftNativeAgentDenied
	}
	var instruction string
	err = tx.QueryRowContext(ctx, `SELECT m.content FROM conversation_messages m
 JOIN agents a ON a.id=m.agent_id AND a.workspace_id=? AND a.crew_id=? AND a.deleted_at IS NULL AND a.expired_at IS NULL
 JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL
 WHERE m.id=? AND m.session_id=? AND m.agent_id=? AND m.author_user_id=? AND m.role='user'
 AND length(CAST(m.content AS BLOB))<=?`, g.Scope.WorkspaceID, g.CrewID, g.MessageID, g.Original.ChatID, g.AgentID, g.Source.UserID, mooring.MaxRPCFrame).Scan(&instruction)
	if err != nil || instruction != g.Instruction {
		return errCraftNativeAgentDenied
	}
	// Rechecked on every agent write: excluding the crew or disabling the
	// policy stops a task already running (#4681).
	if !m.human.pages.artifactAgentPolicyAllows(context.WithValue(ctx, pageAuthReaderKey{}, tx), g.Scope.WorkspaceID, g.CrewID) {
		return errCraftNativeAgentDenied
	}
	epochs, err := craftNativeAgentEpochs(ctx, tx, g, false)
	if err != nil || epochs != g.Epochs {
		return errCraftNativeAgentDenied
	}
	return nil
}

// Reuse the existing canonical non-ABA authority counters, without a D1
// document/application subject. Only fresh original-human admission creates a
// baseline; checks never reconstruct a missing counter or refresh its value.
func craftNativeAgentEpochs(ctx context.Context, tx *sql.Tx, g craftNativeAgentGrant, capture bool) ([4]string, error) {
	var result [4]string
	for i, k := range [][2]string{{"user", g.Source.UserID}, {"page", g.Scope.ArtifactID}, {"agent", g.AgentID}, {"app", g.CrewID}} {
		if capture {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_authority_generations(workspace_id,scope_kind,scope_id) SELECT ?,?,? WHERE NOT EXISTS(SELECT 1 FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?)`, g.Scope.WorkspaceID, k[0], k[1], g.Scope.WorkspaceID, k[0], k[1]); err != nil {
				return result, err
			}
		}
		if err := tx.QueryRowContext(ctx, `SELECT CAST(revision AS TEXT) FROM mooring_authority_generations WHERE workspace_id=? AND scope_kind=? AND scope_id=?`, g.Scope.WorkspaceID, k[0], k[1]).Scan(&result[i]); err != nil {
			return result, err
		}
		if !chataudience.ValidUserAuthorityRevision(result[i]) {
			return result, fmt.Errorf("%w: original epoch unavailable", errCraftNativeAgentDenied)
		}
	}
	return result, nil
}
