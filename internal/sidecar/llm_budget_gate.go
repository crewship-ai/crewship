package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

var errLLMBudgetAdmission = errors.New("LLM budget admission unavailable; hard-budget traffic requires the restricted broker")

func (p *Proxy) admitLLM(w http.ResponseWriter, r *http.Request, actor string, cred *Credential, provider string) bool {
	if p.onLLMAdmission == nil {
		return true
	} // Explicit standalone Proxy; managed Server always installs the gate.
	id := ""
	if cred != nil {
		id = cred.ID
	}
	if p.onLLMAdmission(r.Context(), actor, id, provider) != nil {
		http.Error(w, errLLMBudgetAdmission.Error(), http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) buildLLMAdmission() func(context.Context, string, string, string) error {
	return func(ctx context.Context, actor, credential, provider string) error {
		if s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.Token == "" || s.ipc.WorkspaceID == "" {
			return errLLMBudgetAdmission
		}
		if actor == "" {
			actor = s.ipc.AgentID
		}
		if actor == "" {
			return errLLMBudgetAdmission
		}
		body, err := json.Marshal(struct {
			Agent      string `json:"agent_id"`
			Credential string `json:"credential_id"`
			Provider   string `json:"provider"`
		}{actor, credential, provider})
		if err != nil {
			return errLLMBudgetAdmission
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ipc.BaseURL+"/api/v1/internal/cost/admit", bytes.NewReader(body))
		if err != nil {
			return errLLMBudgetAdmission
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", s.ipc.Token)
		resp, err := ipcClient.Do(req)
		if err != nil {
			return errLLMBudgetAdmission
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return errLLMBudgetAdmission
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
		if err != nil || len(raw) > 1024 {
			return errLLMBudgetAdmission
		}
		var answer struct {
			Allowed bool `json:"allowed"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&answer) != nil || !answer.Allowed {
			return errLLMBudgetAdmission
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return errLLMBudgetAdmission
		}
		return nil
	}
}
