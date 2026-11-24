package chatbridge

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/work"
)

const preparationBucket = "chat_runtime_preparations"

type runtimePreparation struct {
	Chat  string
	Agent string
	Run   string
}

func (p runtimePreparation) persist(ctx context.Context, state provider.StateProvider, id string) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return state.Set(ctx, preparationBucket, id, raw)
}

// RecoverRuntimePreparations runs under the installation writer before admission.
// Receipts contain only identifiers and cannot execute a request.
func RecoverRuntimePreparations(ctx context.Context, state provider.StateProvider, store *conversation.Store, db *sql.DB) error {
	if state == nil || store == nil {
		return nil
	}
	pending, err := state.List(ctx, preparationBucket)
	if err != nil {
		return err
	}
	var failures []error
	for trigger, raw := range pending {
		var p runtimePreparation
		if err := json.Unmarshal(raw, &p); err != nil {
			failures = append(failures, err)
			continue
		}
		var admitted bool
		if db != nil && p.Run != "" {
			admitted, err = work.NewStore(db).RecoverPreparingChat(ctx, p.Run, p.Chat, p.Agent)
			if err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if !admitted {
			if err := p.interrupt(ctx, store, trigger); err != nil {
				failures = append(failures, fmt.Errorf("preparation %s: %w", trigger, err))
				continue
			}
		}
		if err := state.Delete(ctx, preparationBucket, trigger); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (p runtimePreparation) interrupt(ctx context.Context, store *conversation.Store, trigger string) error {
	messages, err := store.ReadTranscript(ctx, p.Chat)
	if err != nil {
		return err
	}
	id := "msg_preparation_" + trigger
	var found bool
	for _, m := range messages {
		if m.ID == id {
			return store.SyncSession(ctx, p.Chat)
		}
		if m.ID == trigger && m.Role == conversation.RoleUser {
			found = true
		}
	}
	if !found {
		return errors.New("trigger message missing")
	}
	const message = "The server restarted while preparing the environment. Your message was saved and the agent did not start. You can send it again."
	if err = store.Append(ctx, p.Chat, conversation.Message{ID: id, AgentID: p.Agent, Role: conversation.RoleSystem, Content: message, Parts: []conversation.Part{{Type: "error", Content: message}}}); err != nil {
		return err
	}
	return store.SyncSession(ctx, p.Chat)
}
