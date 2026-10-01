package restricteddispatch

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/crewship-ai/crewship/internal/access"
	"github.com/crewship-ai/crewship/internal/encryption"
)

// RunProof is a host-only completed output capability. Its opaque handle is
// never emitted through HTTP/SSE; private workflows persist only Seal's cipher.
type RunProof struct {
	handle  string
	sources []string
}

// MarshalJSON denies accidental publication. Seal is the only persistence API.
func (p RunProof) MarshalJSON() ([]byte, error) { return nil, access.ErrDenied }

func NewRunProof(handle string, sourceIDs []string) RunProof {
	return RunProof{handle, slices.Clone(sourceIDs)}
}
func (p RunProof) ContextIDs() []string { return slices.Clone(p.sources) }
func (p RunProof) Read(ctx context.Context, store access.Store, user, workspace, agent, chat string) ([]access.ContextEntry, error) {
	return store.CompletedContext(ctx, p.handle, user, workspace, agent, chat, p.sources)
}
func (p RunProof) Check(ctx context.Context, store access.Store, user, workspace, agent, chat string) error {
	_, err := p.Read(ctx, store, user, workspace, agent, chat)
	return err
}

func (p RunProof) ReadWorkflow(ctx context.Context, store access.Store, user, workspace, agent, chat, job string) ([]access.ContextEntry, error) {
	entries, err := p.Read(ctx, store, user, workspace, agent, chat)
	if err != nil || store.CheckWorkflowContextAttempt(ctx, p.handle, job) != nil {
		return nil, access.ErrDenied
	}
	return entries, nil
}
func (p RunProof) Seal() (string, error) {
	if p.handle == "" || len(p.sources) == 0 || len(p.sources) > 64 {
		return "", access.ErrDenied
	}
	raw, err := json.Marshal(struct {
		Handle  string   `json:"handle"`
		Sources []string `json:"sources"`
	}{p.handle, p.sources})
	if err != nil {
		return "", err
	}
	return encryption.Encrypt(string(raw))
}
func OpenRunProof(cipher string) (RunProof, error) {
	raw, err := encryption.Decrypt(cipher)
	if err != nil || len(raw) > 32768 {
		return RunProof{}, access.ErrDenied
	}
	var data struct {
		Handle  string   `json:"handle"`
		Sources []string `json:"sources"`
	}
	if json.Unmarshal([]byte(raw), &data) != nil || data.Handle == "" || len(data.Sources) == 0 || len(data.Sources) > 64 {
		return RunProof{}, access.ErrDenied
	}
	return NewRunProof(data.Handle, data.Sources), nil
}
