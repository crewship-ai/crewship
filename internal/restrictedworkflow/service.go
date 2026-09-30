package restrictedworkflow

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

type Executor interface {
	ExecuteRun(context.Context, string, string, string, string, func(string, string) error) error
}
type Service struct {
	db        *sql.DB
	executor  Executor
	lifecycle sync.Mutex
	dispatch  sync.Mutex
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	wake      chan struct{}
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
}

func New(db *sql.DB, executor Executor) (*Service, error) {
	if db == nil || executor == nil {
		return nil, ErrDenied
	}
	return &Service{db: db, executor: executor, wake: make(chan struct{}, 1)}, nil
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
	// Unknown paid work is never automatically replayed on server recovery.
	if _, err := s.db.ExecContext(ctx, `UPDATE access_attempts SET revoked_at=COALESCE(revoked_at,?) WHERE id IN(SELECT origin_attempt_id FROM restricted_workflow_jobs WHERE state='running')`, tsformat.Format(time.Now())); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE restricted_workflow_jobs SET state='failed',finished_at=? WHERE state='running'`, tsformat.Format(time.Now())); err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
			for {
				worked, _ := s.DispatchNext(runCtx)
				if !worked {
					break
				}
			}
		}
	}()
	return nil
}
func (s *Service) Close() error {
	s.lifecycle.Lock()
	cancel := s.cancel
	s.cancel = nil
	s.lifecycle.Unlock()
	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	return nil
}
func (s *Service) notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
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
	return s.admit(ctx, user, workspace, slug, inputs, expectedHash, delay, nil, key)
}
func (s *Service) AdmitPage(ctx context.Context, user, workspace string, action pipeline.PageActionInvocation, inputs map[string]any, keys ...string) (Receipt, error) {
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
	return s.admit(ctx, user, workspace, slug, inputs, "", 0, &action, key)
}
func (s *Service) admit(ctx context.Context, user, workspace, slug string, supplied map[string]any, expectedHash string, delay time.Duration, page *pipeline.PageActionInvocation, key string) (Receipt, error) {
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
	definition, err := compile(recipe)
	if err != nil {
		return receipt, err
	}
	inputs, err := normalizeInputs(definition, supplied)
	if err != nil {
		return receipt, err
	}
	rawInputs, _ := json.Marshal(inputs)
	var agent, profile string
	rows, err := s.db.QueryContext(ctx, `SELECT id,restricted_execution_profile FROM agents WHERE workspace_id=? AND slug=? AND deleted_at IS NULL AND (? IS NULL OR crew_id=?)`, workspace, definition.AgentSlug, crew, crew)
	if err != nil {
		return receipt, ErrDenied
	}
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&agent, &profile); err != nil {
			break
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil || count != 1 || (profile != "responses_text" && profile != "native_api_key") {
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
		idempotency = hash(fmt.Sprintf("%s:%s:%s:%s:%s", source, pipelineID, recipeHash, pageAction, key))
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
	chatID := id()
	if _, err = s.db.ExecContext(ctx, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility,origin) VALUES(?,?,?,?,'private','ROUTINE')`, chatID, workspace, agent, user); err != nil {
		return receipt, err
	}
	handle, origin, err := store.Admit(ctx, user, workspace, agent, chatID, "", nil)
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
	j := job{ID: id(), Workspace: workspace, Principal: user, Member: m.ID, Revision: m.Revision, Pipeline: pipelineID, RecipeHash: recipeHash, Recipe: recipe, Agent: agent, Profile: profile, Chat: chatID, Origin: origin.ID, Handle: ciphertext, Idempotency: idempotency, Source: source, Page: pageID, PageAction: pageAction, PageHash: pageHash, Inputs: string(rawInputs), State: "pending", Created: tsformat.Format(time.Now()), FireAt: tsformat.Format(time.Now().Add(delay)), Expires: tsformat.Format(time.Now().Add(delay + time.Hour))}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	// Acquire the writer before fencing and enqueue, so a declaration/member
	// mutation cannot land between the final check and durable insertion.
	if _, err = tx.ExecContext(ctx, `UPDATE access_attempts SET completed_at=completed_at WHERE id=?`, origin.ID); err != nil {
		return receipt, err
	}
	if err = s.checkJob(ctx, tx, j, handle); err != nil {
		return receipt, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO restricted_workflow_jobs(id,workspace_id,principal_id,member_id,member_revision,pipeline_id,recipe_hash,recipe_json,agent_id,execution_profile,chat_id,origin_attempt_id,origin_handle_ciphertext,source_kind,page_id,page_action_json,page_spec_hash,inputs_json,idempotency_key_hash,state,created_at,fire_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.ID, j.Workspace, j.Principal, j.Member, j.Revision, j.Pipeline, j.RecipeHash, j.Recipe, j.Agent, j.Profile, j.Chat, j.Origin, j.Handle, j.Source, j.Page, j.PageAction, j.PageHash, j.Inputs, j.Idempotency, j.State, j.Created, j.FireAt, j.Expires)
	if err != nil {
		return receipt, err
	}
	if err = tx.Commit(); err != nil {
		return receipt, err
	}
	accepted = true
	s.notify()
	return Receipt{j.ID, chatID, "SCHEDULED"}, nil
}
