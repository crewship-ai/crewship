//go:build linux && !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/restricteddispatch"
	"github.com/crewship-ai/crewship/internal/restrictedpreflight"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
	"github.com/crewship-ai/crewship/internal/restrictedworkflow"
)

var restrictedImageDigest = regexp.MustCompile(`^(sha256:[a-f0-9]{64}|[a-z0-9][a-z0-9._:/-]*@sha256:[a-f0-9]{64})$`)

// A restricted worker is an operator-installed, immutable local image. The
// manager never pulls an image or accepts runtime configuration from a request.
// Without this explicit opt-in, restricted execution remains unavailable.
func startRestrictedTextRuntime(ctx context.Context, db *sql.DB, databasePath string, router *api.Router, noDocker bool, logger *slog.Logger) (func(), error) {
	image := os.Getenv("CREWSHIP_RESTRICTED_RUNTIME_IMAGE")
	nativeImage := os.Getenv("CREWSHIP_RESTRICTED_NATIVE_IMAGE")
	if image == "" && nativeImage == "" {
		return func() {}, nil
	}
	if noDocker || router == nil || db == nil || (image != "" && !restrictedImageDigest.MatchString(image)) || (nativeImage != "" && !restrictedImageDigest.MatchString(nativeImage)) {
		return nil, fmt.Errorf("restricted runtime requires Docker, an API router and an immutable image digest")
	}
	absoluteDB, err := filepath.Abs(databasePath)
	if err != nil || databasePath == "" || databasePath == ":memory:" {
		return nil, fmt.Errorf("restricted runtime requires a persistent instance database")
	}
	authority := restricteddispatch.Authority{Store: access.Store{DB: db}}
	registry := &restrictedExecutor{db: db}
	var managers []*restrictedruntime.Manager
	var workflow *restrictedworkflow.Service
	closeAll := func() {
		if workflow != nil {
			if err := workflow.Close(); err != nil {
				logger.Error("restricted workflow shutdown failed", "error", err)
			}
		}
		for _, manager := range managers {
			if err := manager.Close(); err != nil {
				logger.Error("restricted runtime shutdown failed", "error", err)
			}
		}
	}
	if image != "" {
		manager, err := restrictedruntime.New(filepath.Join(filepath.Dir(absoluteDB), "restricted-runtime"), restrictedruntime.Docker{Image: image}, authority, authority, restrictedruntime.Limits{MemoryBytes: 128 << 20, NanoCPUs: 500000000, PIDs: 32})
		if err != nil {
			return nil, err
		}
		managers = append(managers, manager)
		if err = manager.Reconcile(ctx); err != nil {
			closeAll()
			return nil, fmt.Errorf("reconcile restricted text runtime: %w", err)
		}
		registry.text = &restricteddispatch.TextRunner{Authority: authority, Manager: manager, MaxOutputTokens: 4096}
		logger.Info("restricted text runtime enabled", "image", image)
	}
	if nativeImage != "" {
		docker := restrictedruntime.Docker{Image: nativeImage}
		catalog, err := restrictedruntime.NewFrozenNativeCatalog(filepath.Join(filepath.Dir(absoluteDB), "restricted-native-inputs"), docker, restricteddispatch.ProjectInputSource(authority.Store))
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("initialize restricted project inputs: %w", err)
		}
		manager, err := restrictedruntime.NewNative(filepath.Join(filepath.Dir(absoluteDB), "restricted-native-runtime"), docker, authority, catalog, restrictedruntime.NativeLimits())
		if err != nil {
			closeAll()
			return nil, err
		}
		managers = append(managers, manager)
		if err = manager.Reconcile(ctx); err != nil {
			closeAll()
			return nil, fmt.Errorf("reconcile restricted native runtime: %w", err)
		}
		registry.native = &restricteddispatch.NativeRunner{Authority: authority, Manager: manager, MaxOutputTokens: 4096}
		logger.Info("restricted native scratch runtime enabled", "image", nativeImage)
	}
	router.SetRestrictedTextRunner(registry)
	workflow, err = restrictedworkflow.New(db, registry)
	if err != nil {
		closeAll()
		return nil, err
	}
	// The private queue uses this registry for every step. Unknown started
	// work is reconciled as failed, rather than automatically replayed.
	workflow.SourceChecker = restrictedpreflight.CheckSource
	if err = workflow.Start(ctx); err != nil {
		closeAll()
		return nil, fmt.Errorf("start restricted workflow: %w", err)
	}
	router.SetRestrictedWorkflow(workflow)
	return closeAll, nil
}

type restrictedExecutor struct {
	db           *sql.DB
	text, native api.RestrictedTextExecutor
}

func (r *restrictedExecutor) selectRunner(ctx context.Context, workspace, chat string) (api.RestrictedTextExecutor, error) {
	if r == nil || r.db == nil {
		return nil, access.ErrDenied
	}
	var profile string
	if err := r.db.QueryRowContext(ctx, `SELECT a.restricted_execution_profile FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id WHERE c.id=? AND c.workspace_id=? AND a.deleted_at IS NULL`, chat, workspace).Scan(&profile); err != nil {
		return nil, access.ErrDenied
	}
	var runner api.RestrictedTextExecutor
	switch profile {
	case "responses_text":
		runner = r.text
	case "native_api_key":
		runner = r.native
	}
	if runner == nil {
		return nil, access.ErrDenied
	}
	return runner, nil
}
func (r *restrictedExecutor) Execute(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	runner, err := r.selectRunner(ctx, workspace, chat)
	if err != nil {
		return err
	}
	return runner.Execute(ctx, user, workspace, chat, input, emit)
}
func (r *restrictedExecutor) ExecuteRun(ctx context.Context, user, workspace, chat, input string, emit func(string, string) error) error {
	runner, err := r.selectRunner(ctx, workspace, chat)
	if err != nil {
		return err
	}
	return runner.ExecuteRun(ctx, user, workspace, chat, input, emit)
}
func (r *restrictedExecutor) ExecuteRunWithRights(ctx context.Context, user, workspace, chat, input string, rights []access.Right, emit func(string, string) error) error {
	runner, err := r.selectRunner(ctx, workspace, chat)
	if err != nil {
		return err
	}
	if enhanced, ok := runner.(interface {
		ExecuteRunWithRights(context.Context, string, string, string, string, []access.Right, func(string, string) error) error
	}); ok {
		return enhanced.ExecuteRunWithRights(ctx, user, workspace, chat, input, rights, emit)
	}
	if len(rights) != 0 {
		return access.ErrDenied
	}
	return runner.ExecuteRun(ctx, user, workspace, chat, input, emit)
}

// Workflow targets come from the host's frozen declaration. A delegated target
// can differ from the conversation agent; its runner rechecks the parent slot.
func (r *restrictedExecutor) ExecuteWorkflowRun(ctx context.Context, request restricteddispatch.DelegatedRunRequest, emit func(string, string) error) (restricteddispatch.RunProof, error) {
	if r == nil || r.db == nil || request.Agent == "" {
		return restricteddispatch.RunProof{}, access.ErrDenied
	}
	var profile string
	if r.db.QueryRowContext(ctx, `SELECT a.restricted_execution_profile FROM agents a JOIN chats c ON c.workspace_id=a.workspace_id
 WHERE a.id=? AND a.workspace_id=? AND c.id=? AND a.deleted_at IS NULL AND (?<>'' OR c.agent_id=a.id)`, request.Agent, request.Workspace, request.Chat, request.ParentHandle).Scan(&profile) != nil {
		return restricteddispatch.RunProof{}, access.ErrDenied
	}
	var runner api.RestrictedTextExecutor
	switch profile {
	case "responses_text":
		runner = r.text
	case "native_api_key":
		runner = r.native
	}
	workflow, ok := runner.(interface {
		ExecuteWorkflowRun(context.Context, restricteddispatch.DelegatedRunRequest, func(string, string) error) (restricteddispatch.RunProof, error)
	})
	if !ok {
		return restricteddispatch.RunProof{}, access.ErrDenied
	}
	return workflow.ExecuteWorkflowRun(ctx, request, emit)
}

func (r *restrictedExecutor) projectFileRunner(ctx context.Context, workspace, chat string) (api.RestrictedProjectFileExecutor, error) {
	if r == nil || r.db == nil {
		return nil, access.ErrDenied
	}
	var one int
	if r.db.QueryRowContext(ctx, `SELECT 1 FROM chats c JOIN agents a ON a.id=c.agent_id AND a.workspace_id=c.workspace_id
 WHERE c.id=? AND c.workspace_id=? AND a.deleted_at IS NULL AND a.restricted_execution_profile='native_api_key'`, chat, workspace).Scan(&one) != nil {
		return nil, access.ErrDenied
	}
	native, ok := r.native.(api.RestrictedProjectFileExecutor)
	if !ok {
		return nil, access.ErrDenied
	}
	return native, nil
}

func (r *restrictedExecutor) ExecuteWithProjectFiles(ctx context.Context, user, workspace, chat, input string, versions []string, emit func(string, string) error) error {
	runner, err := r.projectFileRunner(ctx, workspace, chat)
	if err != nil {
		return err
	}
	return runner.ExecuteWithProjectFiles(ctx, user, workspace, chat, input, versions, emit)
}

func (r *restrictedExecutor) ExecuteRunWithProjectFiles(ctx context.Context, user, workspace, chat, input string, versions []string, emit func(string, string) error) error {
	runner, err := r.projectFileRunner(ctx, workspace, chat)
	if err != nil {
		return err
	}
	return runner.ExecuteRunWithProjectFiles(ctx, user, workspace, chat, input, versions, emit)
}

// Capability checking happens before graph admission, including every nested leaf.
func (r *restrictedExecutor) SupportsWorkflowProfile(profile string) bool {
	if r == nil {
		return false
	}
	var runner api.RestrictedTextExecutor
	switch profile {
	case "responses_text":
		runner = r.text
	case "native_api_key":
		runner = r.native
	default:
		return false
	}
	if _, ok := runner.(interface {
		ExecuteWorkflowRun(context.Context, restricteddispatch.DelegatedRunRequest, func(string, string) error) (restricteddispatch.RunProof, error)
	}); !ok {
		return false
	}
	if capable, ok := runner.(interface{ SupportsWorkflowProfile(string) bool }); ok {
		return capable.SupportsWorkflowProfile(profile)
	}
	return profile == "responses_text"
}
