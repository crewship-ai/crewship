package restrictedworkflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/dispatch"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/quiesce"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/tsformat"
	"github.com/crewship-ai/crewship/internal/work"
)

type Executor interface {
	ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error
}
type proofExecutor interface {
	ExecuteWorkflowRun(context.Context, restricteddispatch.DelegatedRunRequest, func(string, string) error) (restricteddispatch.RunProof, error)
}
type Service struct {
	db            *sql.DB
	executor      Executor
	providers     providerAuthority
	lifecycle     sync.Mutex
	dispatch      sync.Mutex
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	dispatcher    *dispatch.Dispatcher
	runtime       *workflowRuntime
	ledger        *work.Store
	SourceChecker func(context.Context, pipeline.PageActionQuery, string) error
}
type Receipt struct {
	ID     string `json:"run_id"`
	ChatID string `json:"chat_id"`
	State  string `json:"status"`
}
type Result struct {
	ID        string            `json:"run_id"`
	State     string            `json:"status"`
	Outputs   map[string]string `json:"step_outputs"`
	CreatedAt string            `json:"created_at"`
}
type job struct {
	ID, Workspace, Principal, Member, Pipeline, RecipeHash, Recipe, Agent, Profile, Chat, Origin, Handle, Idempotency, Source, PageAction, PageHash, Inputs, State, Outputs, Created, FireAt, Expires string
	Page                                                                                                                                                                                              sql.NullString
	Revision                                                                                                                                                                                          int64
	Rights, SourceFacet                                                                                                                                                                               string
	Graph, GraphHash, Proofs                                                                                                                                                                          string
}

func New(db *sql.DB, executor Executor) (*Service, error) {
	if db == nil || executor == nil {
		return nil, ErrDenied
	}
	s := &Service{db: db, executor: executor, providers: workflowProviderAuthority(db), ledger: work.NewStore(db)}
	s.runtime = newWorkflowRuntime(s)
	s.dispatcher = dispatch.New(s.ledger, s.runtime, s.runtime, workflowDispatchConfig(), nil)
	return s, nil
}
func id() string {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	return "c" + hex.EncodeToString(raw[:])
}
func hash(raw string) string { sum := sha256.Sum256([]byte(raw)); return hex.EncodeToString(sum[:]) }
func (s *Service) Start(ctx context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.cancel != nil {
		return ErrDenied
	}
	if _, err := encryption.Encrypt("restricted-workflow-key-check"); err != nil {
		return ErrDenied
	}
	writer, ok := quiesce.Enter(ctx)
	if ok {
		defer writer.Leave()
		if _, err := s.ledger.RecoverExpiredLeases(writer.Context()); err != nil {
			return err
		}
		if err := s.runtime.FlushRunOutcomes(writer.Context()); err != nil {
			return err
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		runWorkflowDispatcher(runCtx, s.dispatcher.Run)
	}()
	return nil
}

func runWorkflowDispatcher(ctx context.Context, run func(context.Context) error) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := run(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		if errors.Is(err, dispatch.ErrNoExecutableKinds) {
			slog.Error("restricted workflow dispatcher stopped", "error", err)
			return
		}
		slog.Error("restricted workflow recovery failed; retrying", "error", err, "backoff", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (s *Service) Close() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	cancel := s.cancel
	s.cancel = nil
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}
func (s *Service) notify() { s.dispatcher.Hint("") }

func manualPolicy(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, user, workspace string) error {
	var role string
	var caps sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT role,capabilities FROM workspace_members WHERE user_id=? AND workspace_id=?`, user, workspace).Scan(&role, &caps); err != nil {
		return ErrDenied
	}
	if role == "OWNER" || role == "ADMIN" || role == "MANAGER" {
		return nil
	}
	var list []string
	if caps.Valid && json.Unmarshal([]byte(caps.String), &list) == nil {
		for _, capability := range list {
			if capability == pipeline.RoutineRunAuthority {
				return nil
			}
		}
	}
	return ErrDenied
}
func (s *Service) AdmitManual(ctx context.Context, user, workspace, slug string, inputs map[string]any, expectedHash string, delay time.Duration, keys ...string) (Receipt, error) {
	return s.AdmitManualFrozen(ctx, user, workspace, slug, inputs, expectedHash, "", delay, keys...)
}

// AdmitManualFrozen binds an optional catalog fingerprint to all nested recipes and provider slots.
func (s *Service) AdmitManualFrozen(ctx context.Context, user, workspace, slug string, inputs map[string]any, expectedHash, expectedExecutionHash string, delay time.Duration, keys ...string) (Receipt, error) {
	if delay < 0 || delay > 24*time.Hour {
		return Receipt{}, ErrDenied
	}
	if err := manualPolicy(ctx, s.db, user, workspace); err != nil {
		return Receipt{}, err
	}
	key := ""
	if len(keys) > 1 {
		return Receipt{}, ErrDenied
	}
	if len(keys) == 1 {
		key = keys[0]
	}
	return s.admit(ctx, user, workspace, slug, inputs, expectedHash, delay, nil, key, nil, expectedExecutionHash)
}
func (s *Service) AdmitPage(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation, inputs map[string]any, keys ...string) (Receipt, error) {
	return s.AdmitPageFrozen(ctx, user, workspace, action, inputs, "", keys...)
}

// AdmitPageFrozen checks the caller-observed Page intent before creating execution authority.
func (s *Service) AdmitPageFrozen(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation, inputs map[string]any, expectedIntent string, keys ...string) (Receipt, error) {
	if err := pipeline.CheckDeclaredPageAction(ctx, s.db, user, workspace, action); err != nil {
		return Receipt{}, ErrDenied
	}
	var slug string
	if err := s.db.QueryRowContext(ctx, `SELECT slug FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, action.PipelineID, workspace).Scan(&slug); err != nil {
		return Receipt{}, ErrDenied
	}
	key := ""
	if len(keys) > 1 {
		return Receipt{}, ErrDenied
	}
	if len(keys) == 1 {
		key = keys[0]
	}
	return s.admit(ctx, user, workspace, slug, inputs, "", 0, &action, key, nil, expectedIntent)
}
func (s *Service) admit(ctx context.Context, user, workspace, slug string, supplied map[string]any, expectedHash string, delay time.Duration, page *pipeline.PageActionInvocation, key string, prepared *PreparedInvocation, expectedExecutions ...string) (Receipt, error) {
	if len(expectedExecutions) > 1 {
		return Receipt{}, ErrDenied
	}
	var receipt Receipt
	store := access.Store{DB: s.db}
	m, err := store.Membership(ctx, user, workspace)
	if err != nil || m.Mode != "restricted" {
		return receipt, ErrDenied
	}
	var pipelineID, recipe string
	var crew sql.NullString
	if err = s.db.QueryRowContext(ctx, `SELECT id,definition_json,author_crew_id FROM pipelines WHERE workspace_id=? AND slug=? AND deleted_at IS NULL AND status='active'`, workspace, slug).Scan(&pipelineID, &recipe, &crew); err != nil {
		return receipt, ErrDenied
	}
	recipeHash := hash(recipe)
	if expectedHash != "" && expectedHash != recipeHash {
		return receipt, ErrDenied
	}
	graph, err := s.compileGraph(ctx, user, workspace, pipelineID)
	if err != nil {
		return receipt, err
	}
	if _, ok := s.executor.(proofExecutor); !ok {
		return receipt, ErrUnsupported
	}
	definition, err := compileTyped(recipe, true)
	if err != nil {
		return receipt, err
	}
	inputs, err := normalizeInputs(definition, supplied)
	if err != nil {
		return receipt, err
	}
	rawInputs, _ := json.Marshal(inputs)
	agent, profile := graph.Agent, graph.Profile
	rawGraph, err := json.Marshal(graph)
	if err != nil {
		return receipt, ErrDenied
	}
	graphHash := hash(string(rawGraph))
	intentHash := graphHash
	if page != nil {
		intentHash = pageIntentHash(*page, graphHash)
	}
	if len(expectedExecutions) == 1 && expectedExecutions[0] != "" && expectedExecutions[0] != intentHash {
		return receipt, ErrDenied
	}
	if err = store.Check(ctx, user, workspace, access.Right{Kind: "agent", ID: agent, Operation: "run"}); err != nil {
		return receipt, ErrDenied
	}
	pageID := sql.NullString{}
	pageAction, pageHash, source := "", "", "manual"
	if page != nil {
		if page.PipelineID != pipelineID {
			return receipt, ErrDenied
		}
		source = "page"
		pageID = sql.NullString{String: page.PageID, Valid: true}
		data, _ := json.Marshal(page)
		pageAction = string(data)
		var spec string
		if err = s.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id=? AND workspace_id=?`, page.PageID, workspace).Scan(&spec); err != nil {
			return receipt, ErrDenied
		}
		pageHash = hash(spec)
	}
	idempotency := ""
	if len(key) > 128 {
		return receipt, ErrDenied
	}
	if key != "" {
		idempotency = hash(fmt.Sprintf("%s:%s:%s:%s:%s", source, pipelineID, graphHash, pageAction, key))
		var existing string
		err = s.db.QueryRowContext(ctx, `SELECT id FROM restricted_workflow_jobs WHERE workspace_id=? AND principal_id=? AND idempotency_key_hash=?`, workspace, user, idempotency).Scan(&existing)
		if err == nil {
			old, loadErr := s.load(ctx, existing)
			if loadErr != nil || old.Inputs != string(rawInputs) {
				return receipt, ErrDenied
			}
			h, decryptErr := encryption.Decrypt(old.Handle)
			if decryptErr != nil || s.checkJob(ctx, s.db, old, h) != nil {
				return receipt, ErrDenied
			}
			return Receipt{old.ID, old.Chat, "DEDUPED"}, nil
		}
		if err != sql.ErrNoRows {
			return receipt, err
		}
	}
	var rights []access.Right
	facet := ""
	if prepared != nil {
		rights = prepared.rights
		facet = prepared.facet
	}
	rights = append(append([]access.Right(nil), rights...), graph.Rights...)
	rawRights, _ := json.Marshal(rights)
	chatID := id()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,origin) VALUES(?,?,?,?,'private','ROUTINE')`, chatID, workspace, agent, user); err != nil {
		return receipt, err
	}
	handle, origin, err := store.Admit(ctx, user, workspace, agent, chatID, "", rights)
	if err != nil {
		return receipt, ErrDenied
	}
	accepted := false
	defer func() {
		if !accepted {
			clean, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = store.RevokeAttempt(clean, handle)
		}
	}()
	if origin.Member != m.ID || origin.Revision != m.Revision {
		return receipt, ErrDenied
	}
	// The completed origin is historical provenance, never executable authority.
	sourceText, _ := json.Marshal(map[string]any{"routine": slug, "recipeHash": recipeHash, "inputs": inputs})
	if _, err = store.AppendContext(ctx, handle, access.ContextUser, string(sourceText)); err != nil {
		return receipt, err
	}
	if err = store.CompleteAttempt(ctx, handle); err != nil {
		return receipt, err
	}
	ciphertext, err := encryption.Encrypt(handle)
	if err != nil {
		return receipt, ErrDenied
	}
	j := job{ID: id(), Workspace: workspace, Principal: user, Member: m.ID, Revision: m.Revision, Pipeline: pipelineID, RecipeHash: recipeHash, Recipe: recipe, Agent: agent, Profile: profile, Chat: chatID, Origin: origin.ID, Handle: ciphertext, Idempotency: idempotency, Source: source, Page: pageID, PageAction: pageAction, PageHash: pageHash, Inputs: string(rawInputs), Rights: string(rawRights), SourceFacet: facet, State: "pending", Created: tsformat.Format(time.Now()), FireAt: tsformat.Format(time.Now().Add(delay)), Expires: tsformat.Format(time.Now().Add(delay + time.Hour))}
	j.Graph, j.GraphHash, j.Proofs = string(rawGraph), graphHash, "{}"
	if prepared != nil {
		prepared.job = j
		prepared.handle = handle
		prepared.owner = s
		accepted = true
		return Receipt{j.ID, j.Chat, "PREPARED"}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	receipt, err = s.EnqueuePrepared(ctx, tx, &PreparedInvocation{owner: s, job: j, handle: handle})
	if err != nil {
		return Receipt{}, err
	}
	if err = tx.Commit(); err != nil {
		return receipt, err
	}
	accepted = true
	s.notify()
	return Receipt{j.ID, chatID, "SCHEDULED"}, nil
}
