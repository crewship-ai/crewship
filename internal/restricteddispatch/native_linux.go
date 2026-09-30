//go:build linux

package restricteddispatch

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/restrictedruntime"
)

// NativePrompt is produced by the scoped host context builder after admission.
// Neither field may come directly from worker configuration or ambient history.
type NativePrompt struct{ Instructions, Input string }
type BuildNativePrompt func(context.Context, access.Attempt) (NativePrompt, error)

// PrepareNative is a separate API-key-only, run-operation native profile. It
// freezes scoped context before launch and cannot enable an agent's profile.
func (a Authority) PrepareNative(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, maxOutputTokens int64, build BuildNativePrompt) (string, access.Attempt, error) {
	return a.prepareNative(ctx, user, workspace, agent, chat, parent, rights, maxOutputTokens, build, false)
}

func (a Authority) PrepareChatNative(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, maxOutputTokens int64, build BuildNativePrompt) (string, access.Attempt, error) {
	return a.prepareNative(ctx, user, workspace, agent, chat, parent, rights, maxOutputTokens, build, true)
}

func (a Authority) prepareNative(ctx context.Context, user, workspace, agent, chat, parent string, rights []access.Right, maxOutputTokens int64, build BuildNativePrompt, chatOperation bool) (string, access.Attempt, error) {
	if build == nil || maxOutputTokens < 1 || maxOutputTokens > 32768 {
		return "", access.Attempt{}, access.ErrDenied
	}
	return a.prepare(ctx, user, workspace, agent, chat, parent, rights, func(ctx context.Context, attempt access.Attempt) ([]string, error) {
		expected := "run"
		if chatOperation {
			expected = "chat"
		}
		if attempt.AdmissionOperation != expected {
			return nil, access.ErrDenied
		}
		if e := a.pinProvider(ctx, attempt, maxOutputTokens); e != nil {
			return nil, e
		}
		binding, e := loadProvider(ctx, a.Store.DB, attempt.ID)
		if e != nil || binding.Profile != "native_api_key" {
			return nil, access.ErrDenied
		}
		if _, known := restrictedruntime.NativeContextCeiling(binding.Model); !known {
			return nil, access.ErrDenied
		}
		var profile string
		if e := a.Store.DB.QueryRowContext(ctx, `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, attempt.Agent, attempt.Workspace).Scan(&profile); e != nil || profile != "native_api_key" {
			return nil, access.ErrDenied
		}
		prompt, e := build(ctx, attempt)
		if e != nil {
			return nil, e
		}
		return a.freezeNativePrompt(ctx, attempt, prompt)
	}, chatOperation)
}

// freezeNativePrompt is shared by direct and host-delegated native builders.
// The caller pins the exact provider and binds/imports classified sources first.
func (a Authority) freezeNativePrompt(ctx context.Context, attempt access.Attempt, prompt NativePrompt) ([]string, error) {
	var err error
	prompt, err = a.nativeProjectPrompt(ctx, attempt, prompt)
	if err != nil {
		return nil, err
	}
	binding, err := loadProvider(ctx, a.Store.DB, attempt.ID)
	if err != nil || binding.Profile != "native_api_key" || (attempt.AdmissionOperation != "run" && attempt.AdmissionOperation != "chat") {
		return nil, access.ErrDenied
	}
	if _, known := restrictedruntime.NativeContextCeiling(binding.Model); !known {
		return nil, access.ErrDenied
	}
	var profile string
	if err := a.Store.DB.QueryRowContext(ctx, `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, attempt.Agent, attempt.Workspace).Scan(&profile); err != nil || profile != "native_api_key" {
		return nil, access.ErrDenied
	}
	if !utf8.ValidString(prompt.Instructions) || !utf8.ValidString(prompt.Input) || len(prompt.Instructions) > 128<<10 || len(prompt.Input) > 512<<10 || prompt.Input == "" {
		return nil, access.ErrDenied
	}
	if _, err := a.Store.DB.ExecContext(ctx, `INSERT INTO restricted_native_sessions(attempt_id,workspace_id,principal_id,agent_id,scope_id,context_revision,instructions,initial_input) VALUES(?,?,?,?,?,?,?,?)`, attempt.ID, attempt.Workspace, attempt.Principal, attempt.Agent, attempt.Scope, attempt.Revision, prompt.Instructions, prompt.Input); err != nil {
		return nil, err
	}
	return []string{"/opt/crewship-native-runner", "native", binding.Model}, nil
}

func (a Authority) attachNative(ctx context.Context, attempt access.Attempt, plan *restrictedruntime.Plan) error {
	var exists int
	e := a.Store.DB.QueryRowContext(ctx, `SELECT 1 FROM restricted_native_sessions WHERE attempt_id=? AND workspace_id=? AND principal_id=? AND agent_id=? AND scope_id=? AND context_revision=?`, attempt.ID, attempt.Workspace, attempt.Principal, attempt.Agent, attempt.Scope, attempt.Revision).Scan(&exists)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if len(plan.Command) != 3 || plan.Command[0] != "/opt/crewship-native-runner" || plan.Command[1] != "native" || plan.Network == nil || len(plan.Network.Grants) != 1 || plan.Network.Grants[0].Responses == nil {
		return access.ErrDenied
	}
	binding, e := loadProvider(ctx, a.Store.DB, attempt.ID)
	if e != nil {
		return e
	}
	if binding.Model != plan.Command[2] || binding.Profile != "native_api_key" || (attempt.AdmissionOperation != "run" && attempt.AdmissionOperation != "chat") {
		return access.ErrDenied
	}
	var profile string
	if e = a.Store.DB.QueryRowContext(ctx, `SELECT restricted_execution_profile FROM agents WHERE id=? AND workspace_id=? AND deleted_at IS NULL`, attempt.Agent, attempt.Workspace).Scan(&profile); e != nil || profile != "native_api_key" {
		return access.ErrDenied
	}
	ceiling, known := restrictedruntime.NativeContextCeiling(binding.Model)
	if !known {
		return access.ErrDenied
	}
	plan.Network.Grants[0].Native = &restrictedruntime.NativePolicy{Model: binding.Model, MaxOutputTokens: binding.MaxOutputTokens, InputTokenCeiling: ceiling}
	plan.Network.Grants[0].Responses = nil
	plan.NativeSandbox = restrictedruntime.NativeSandboxFingerprint()
	manifest, e := a.nativeInputManifest(ctx, attempt)
	if e != nil {
		return e
	}
	if manifest != nil {
		plan.NativeInputs = manifest
		plan.Mounts = []restrictedruntime.Mount{{Resource: restrictedruntime.NativeInputResource(*plan), Target: restrictedruntime.NativeInputTarget, ReadOnly: true}}
	}
	return nil
}

// NativeRequest claims a durable one-shot lease before any upstream dispatch.
// An interrupted request leaves this lease present and fails future execution.
func (a Authority) NativeRequest(ctx context.Context, handle, credential string, raw []byte) ([]byte, string, error) {
	attempt, e := a.Store.Resolve(ctx, handle)
	if e != nil {
		return nil, "", e
	}
	binding, e := loadProvider(ctx, a.Store.DB, attempt.ID)
	if e != nil || binding.Credential != credential {
		return nil, "", access.ErrDenied
	}
	if _, e = a.checkedProvider(ctx, attempt, binding); e != nil {
		return nil, "", e
	}
	conn, e := a.Store.DB.Conn(ctx)
	if e != nil {
		return nil, "", e
	}
	defer conn.Close()
	if _, e = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		return nil, "", e
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") //nolint:errcheck // successful commit has already closed the transaction
	var state restrictedruntime.NativeConversation
	var prefix, history string
	var lease sql.NullString
	e = conn.QueryRowContext(ctx, `SELECT instructions,initial_input,client_prefix_json,history_json,in_flight_lease FROM restricted_native_sessions WHERE attempt_id=? AND workspace_id=? AND principal_id=? AND agent_id=? AND scope_id=? AND context_revision=?`, attempt.ID, attempt.Workspace, attempt.Principal, attempt.Agent, attempt.Scope, attempt.Revision).Scan(&state.Instructions, &state.Input, &prefix, &history, &lease)
	if e != nil || lease.Valid || len(prefix) > 1<<20 || len(history) > 1<<20 || json.Unmarshal([]byte(prefix), &state.Prefix) != nil || json.Unmarshal([]byte(history), &state.History) != nil {
		return nil, "", access.ErrDenied
	}
	body, next, e := (restrictedruntime.NativePolicy{Model: binding.Model, MaxOutputTokens: binding.MaxOutputTokens}).NativeRequestBody(raw, state)
	if e != nil {
		return nil, "", e
	}
	var nonce [32]byte
	if _, e = rand.Read(nonce[:]); e != nil {
		return nil, "", e
	}
	ticket := hex.EncodeToString(nonce[:])
	prefixJSON, e := json.Marshal(next.Prefix)
	if e != nil {
		return nil, "", e
	}
	historyJSON, e := json.Marshal(next.History)
	if e != nil || len(prefixJSON) > 1<<20 || len(historyJSON) > 1<<20 {
		return nil, "", access.ErrDenied
	}
	if _, e = conn.ExecContext(ctx, `UPDATE restricted_native_sessions SET client_prefix_json=?,history_json=?,in_flight_lease=? WHERE attempt_id=? AND in_flight_lease IS NULL`, string(prefixJSON), string(historyJSON), ticket, attempt.ID); e != nil {
		return nil, "", e
	}
	if _, e = conn.ExecContext(ctx, "COMMIT"); e != nil {
		return nil, "", e
	}
	return body, ticket, nil
}

func (a Authority) NativeComplete(ctx context.Context, handle, ticket string, issued []json.RawMessage) error {
	attempt, e := a.Store.Resolve(ctx, handle)
	if e != nil {
		return e
	}
	if ticket == "" || len(issued) == 0 || len(issued) > 64 {
		return access.ErrDenied
	}
	conn, e := a.Store.DB.Conn(ctx)
	if e != nil {
		return e
	}
	defer conn.Close()
	if _, e = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
		return e
	}
	defer conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") //nolint:errcheck // successful commit has already closed the transaction
	var historyJSON string
	if e = conn.QueryRowContext(ctx, `SELECT history_json FROM restricted_native_sessions WHERE attempt_id=? AND workspace_id=? AND principal_id=? AND agent_id=? AND scope_id=? AND context_revision=? AND in_flight_lease=?`, attempt.ID, attempt.Workspace, attempt.Principal, attempt.Agent, attempt.Scope, attempt.Revision, ticket).Scan(&historyJSON); e != nil {
		return access.ErrDenied
	}
	var history []json.RawMessage
	if len(historyJSON) > 1<<20 || json.Unmarshal([]byte(historyJSON), &history) != nil {
		return access.ErrDenied
	}
	ids := map[string]bool{}
	calls := map[string]bool{}
	for _, items := range [][]json.RawMessage{history, issued} {
		for _, raw := range items {
			var item map[string]any
			if json.Unmarshal(raw, &item) != nil {
				return access.ErrDenied
			}
			id, _ := item["id"].(string)
			if id != "" {
				if ids[id] {
					return access.ErrDenied
				}
				ids[id] = true
			}
			if item["type"] == "function_call" {
				call, _ := item["call_id"].(string)
				if call == "" || calls[call] {
					return access.ErrDenied
				}
				calls[call] = true
			}
		}
	}
	history = append(history, issued...)
	encoded, e := json.Marshal(history)
	if e != nil || len(encoded) > 1<<20 {
		return access.ErrDenied
	}
	if _, e = conn.ExecContext(ctx, `UPDATE restricted_native_sessions SET history_json=?,in_flight_lease=NULL WHERE attempt_id=? AND in_flight_lease=?`, string(encoded), attempt.ID, ticket); e != nil {
		return e
	}
	_, e = conn.ExecContext(ctx, "COMMIT")
	return e
}
