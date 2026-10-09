package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var errLLMBudgetAdmission = errors.New("LLM budget admission unavailable; hard-budget traffic requires the restricted broker")

// llmAdmissionFailure names why an LLM request was not admitted (#2899).
// Every kind refuses the request — admission stays fail-closed — but they ask
// different people to do different things, and before this they all read as
// a hard-budget refusal (403) even on a workspace with no budget at all.
type llmAdmissionFailure string

const (
	// The sidecar has no usable host binding (IPC URL, token, workspace or
	// identity). An installation problem.
	admissionConfiguration llmAdmissionFailure = "configuration"
	// The host's admission endpoint could not be reached from the container:
	// connection refused, timeout, DNS. Typically a host firewall rule.
	admissionTransport llmAdmissionFailure = "transport"
	// The host refused the sidecar's internal credential or network origin.
	admissionAuthentication llmAdmissionFailure = "authentication"
	// The host answered but could not evaluate its budgets.
	admissionHostUnavailable llmAdmissionFailure = "host-unavailable"
	// The host answered something this sidecar does not understand.
	admissionProtocol llmAdmissionFailure = "protocol"
	// The host evaluated the request and refused this agent, crew or
	// credential scope.
	admissionPolicy llmAdmissionFailure = "policy"
	// The host evaluated the request and a hard budget applies.
	admissionBudget llmAdmissionFailure = "budget"
)

// llmAdmissionError is a refused admission. Error() is what the agent sees;
// detail is for the sidecar log only (it may name host addresses).
type llmAdmissionError struct {
	kind   llmAdmissionFailure
	reason string
	detail string
}

func (e *llmAdmissionError) Error() string {
	switch e.kind {
	case admissionBudget:
		return errLLMBudgetAdmission.Error()
	case admissionPolicy:
		return "LLM request refused by host policy: " + e.reason
	case admissionConfiguration:
		return "LLM admission unavailable (configuration): this sidecar has no host admission binding; the request was not sent"
	case admissionTransport:
		return "LLM admission unavailable (transport): the admission host is unreachable from the container — check the host firewall and network; the request was not sent"
	case admissionAuthentication:
		return "LLM admission unavailable (authentication): the host refused this sidecar's internal credential; the request was not sent"
	case admissionHostUnavailable:
		return "LLM admission unavailable: the host could not evaluate budgets; the request was not sent"
	default:
		return "LLM admission unavailable (protocol): unexpected admission response from the host; the request was not sent"
	}
}

// status is the HTTP status the agent receives. Only a decision the host
// actually made is a 403; everything that kept the host from deciding is a
// 502/503 so it is not mistaken for a budget or permission verdict.
func (e *llmAdmissionError) status() int {
	switch e.kind {
	case admissionBudget, admissionPolicy:
		return http.StatusForbidden
	case admissionProtocol:
		return http.StatusBadGateway
	default:
		return http.StatusServiceUnavailable
	}
}

func admissionFailed(kind llmAdmissionFailure, detail string) error {
	return &llmAdmissionError{kind: kind, detail: detail}
}

func (p *Proxy) admitLLM(w http.ResponseWriter, r *http.Request, actor string, cred *Credential, provider string) bool {
	if p.onLLMAdmission == nil {
		return true
	} // Explicit standalone Proxy; managed Server always installs the gate.
	id := ""
	if cred != nil {
		id = cred.ID
	}
	if err := p.onLLMAdmission(r.Context(), actor, id, provider); err != nil {
		status := http.StatusForbidden
		msg := errLLMBudgetAdmission.Error()
		var refused *llmAdmissionError
		if errors.As(err, &refused) {
			status, msg = refused.status(), refused.Error()
			if p.logger != nil {
				p.logger.Warn("LLM admission refused", "kind", string(refused.kind), "provider", provider, "detail", refused.detail)
			}
		}
		http.Error(w, msg, status)
		return false
	}
	return true
}

func (s *Server) buildLLMAdmission() func(context.Context, string, string, string) error {
	return func(ctx context.Context, actor, credential, provider string) error {
		if s.ipc == nil || s.ipc.BaseURL == "" || s.ipc.Token == "" || s.ipc.WorkspaceID == "" {
			return admissionFailed(admissionConfiguration, "IPC base URL, token or workspace missing")
		}
		if actor == "" {
			actor = s.ipc.AgentID
		}
		var body []byte
		var err error
		switch {
		case actor != "":
			body, err = json.Marshal(struct {
				Agent      string `json:"agent_id"`
				Credential string `json:"credential_id"`
				Provider   string `json:"provider"`
			}{actor, credential, provider})
		case s.ipc.CrewOnly && s.ipc.CrewID != "":
			// The crew-level sidecar of a routine script step (#2761) has no
			// agent; it asks under the crew its token is bound to. Only a
			// sidecar started crew-only may do this: an agent sidecar that
			// lost its agent id stays closed rather than borrowing the crew.
			body, err = json.Marshal(struct {
				Crew       string `json:"crew_id"`
				Credential string `json:"credential_id"`
				Provider   string `json:"provider"`
			}{s.ipc.CrewID, credential, provider})
		default:
			return admissionFailed(admissionConfiguration, "no agent identity and not a crew-only sidecar")
		}
		if err != nil {
			return admissionFailed(admissionConfiguration, "encode admission request: "+err.Error())
		}
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.ipc.BaseURL+"/api/v1/internal/cost/admit", bytes.NewReader(body))
		if err != nil {
			return admissionFailed(admissionConfiguration, "build admission request: "+err.Error())
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Token", s.ipc.Token)
		resp, err := ipcClient.Do(req)
		if err != nil {
			return admissionFailed(admissionTransport, err.Error())
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1025))
		if err != nil || len(raw) > 1024 {
			return admissionFailed(admissionProtocol, fmt.Sprintf("HTTP %d: unreadable or oversized body", resp.StatusCode))
		}
		if resp.StatusCode != http.StatusOK {
			return classifyAdmissionRefusal(resp.StatusCode, raw)
		}
		var answer struct {
			Allowed bool `json:"allowed"`
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if dec.Decode(&answer) != nil {
			return admissionFailed(admissionProtocol, "HTTP 200 with an undecodable admission answer")
		}
		var extra any
		if dec.Decode(&extra) != io.EOF {
			return admissionFailed(admissionProtocol, "HTTP 200 with trailing data after the admission answer")
		}
		if !answer.Allowed {
			return &llmAdmissionError{kind: admissionPolicy, reason: "not allowed", detail: "HTTP 200 allowed=false"}
		}
		return nil
	}
}

// hostBudgetRefusal is the host's own wording for a hard-budget refusal
// (internal/api/internal_cost_admit.go). It is the only 403 that is a budget.
const hostBudgetRefusal = "hard-budget traffic requires the restricted broker"

// classifyAdmissionRefusal maps a non-200 host answer to the failure kind.
func classifyAdmissionRefusal(status int, raw []byte) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(raw, &body)
	detail := fmt.Sprintf("HTTP %d: %q", status, body.Error)
	switch {
	case status == http.StatusForbidden && body.Error == hostBudgetRefusal:
		return &llmAdmissionError{kind: admissionBudget, detail: detail}
	case status == http.StatusUnauthorized, status == http.StatusNotFound,
		status == http.StatusForbidden && body.Error == "Forbidden":
		// The internal auth middleware answers a bare "Forbidden" for a token
		// it cannot validate (or a cross-workspace/crew one), and 404
		// (deliberately opaque) for a refused network origin.
		return &llmAdmissionError{kind: admissionAuthentication, detail: detail}
	case status == http.StatusForbidden && body.Error != "" && len(body.Error) <= 120:
		// Scope refusals: "agent scope unavailable", "crew scope mismatch",
		// "credential scope unavailable", "workspace-bound internal token
		// required". Host-authored fixed sentences, safe to relay.
		return &llmAdmissionError{kind: admissionPolicy, reason: body.Error, detail: detail}
	case status == http.StatusServiceUnavailable:
		return &llmAdmissionError{kind: admissionHostUnavailable, detail: detail}
	default:
		return &llmAdmissionError{kind: admissionProtocol, detail: detail}
	}
}
