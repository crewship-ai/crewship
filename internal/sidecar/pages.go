package sidecar

// Pages — the producer door for a process running INSIDE a crew container
// (docs/prd/pages.md §0, §4, §7.1 rule 4, §7.1b; issue #1946).
//
//	PUT /pages/{page}/{panel}                 body: the panel's payload
//	PUT /pages/{page}/{panel}?state=failed    the producer's own verdict
//
// Why this exists at all, given the public route already works: it works by
// handing the container a CLI token. That is precisely the credential the
// sidecar exists to keep out of the agent process. Until this route, §4 rule 5
// — "identity is attached by the server from the token, never taken from the
// body" — held for humans and for routines and was merely *hoped for* in a
// container, because the only thing standing between an agent and a forged
// producer was that the agent had not thought to try.
//
// The server half was already complete. mayProduceUnattended
// (internal/api/pages_internal.go) has had a `case "agent"` arm and the
// produce-grant fallback since #1945, and author_run_id has always been
// optional there — the comment above resolveProducerRun names "a script in a
// crew container" as a caller with no run. What was missing was only the door.
//
// The shape mirrors the PUBLIC producer contract rather than the dispatcher's:
// the body IS the payload, and `state` rides the query string. That is
// deliberate. An envelope is forced on the routine path because the dispatcher
// must write identity into the body, having nowhere else to put it. Here the
// token carries identity, so an envelope would buy nothing and would give the
// feature a second body shape for producers to get wrong. What an agent reads
// in docs/guides/pages.mdx about pushing from a shell is true here too, minus
// the token.
//
// Everything this handler adds to the request is identity, and none of it is
// negotiable:
//
//	workspace_id, crew_id   from the IPC config
//	agent_id                from the ACTING per-agent token (#812), so a
//	                        sibling sharing the sidecar cannot push as the
//	                        boot agent, and a forged token pushes as nobody
//	author_run_id           deliberately ABSENT — an agent is not a run
//
// It adds no judgement. Whether this agent may write this panel is
// §7.1 rule 4's question and the server answers it; whether the payload is
// legal is the schema's and pages.ValidatePayload answers it. The refusal
// travels back byte-for-byte (§11b.6) so the sentence an agent reads is the
// sentence a shell script reads.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

// pagePushMaxBodyBytes bounds what this handler will buffer from the agent.
//
// It is NOT the payload cap. The payload cap is pages.MaxPayloadBytes and it
// belongs to the server, which refuses an over-cap payload with a rejection
// naming the bytes attempted and the limit — the same rejection every other
// producer gets. Re-deciding that here would give one rule two implementations,
// which is how §11b.6 gets quietly broken.
//
// What this bounds is the sidecar's own memory. The sidecar runs as the
// container's egress and credential boundary, and a compromised agent PUTting
// a multi-gigabyte body at an unbounded io.ReadAll would OOM it (#1046, #1058).
// The slack above the payload cap is what keeps the two concerns separate: any
// body a producer could legitimately send — including one a few KiB over the
// cap — is forwarded and gets the SERVER's answer. Only a pathological body
// stops here, and it stops with a 413 that names the real limit.
const pagePushMaxBodyBytes = int64(pages.MaxPayloadBytes) + (8 << 10)

// pagePushTimeout bounds the upstream call. A push is one INSERT and a
// websocket fan-out; if crewshipd has not answered in this long, the agent is
// better served by an error it can retry than by a hung tool call.
const pagePushTimeout = 20 * time.Second

// handlePagePush forwards one panel write to crewshipd's internal page route
// with the caller's identity attached.
//
// PUT /pages/{page}/{panel}
func (s *Server) handlePagePush(w http.ResponseWriter, r *http.Request) {
	if s.ipc == nil {
		writeJSONResponse(w, http.StatusServiceUnavailable, map[string]string{"error": "IPC not configured"})
		return
	}

	page, panel, ok := splitPagePanelPath(r.URL.Path)
	if !ok {
		writeJSONResponse(w, http.StatusNotFound, map[string]string{
			"error": "path must be /pages/{page}/{panel} — one push writes one panel",
		})
		return
	}

	// #812: the ACTING agent, not the boot agent of a shared sidecar. A forged
	// token, or a missing one where tokens are provisioned, pushes as nobody.
	agentID, ok := s.actingAgentID(r)
	if !ok {
		writeJSONResponse(w, http.StatusForbidden, map[string]string{"error": "unrecognized agent token"})
		return
	}

	payload, ok := readPagePayload(w, r)
	if !ok {
		return
	}

	// The envelope the internal route expects. Note what is not here: no
	// produced_at, no run, no freshness. §4 rules 2 and 5 make all three the
	// server's, and a producer that could set them could claim to have
	// measured something it never measured.
	env := map[string]any{
		"workspace_id": s.ipc.WorkspaceID,
		"crew_id":      s.ipc.CrewID,
		"agent_id":     agentID,
		"panel":        panel,
		"data":         json.RawMessage(payload),
	}
	// §4 rule 2 — "ok" or "failed" is the only part of its own state a producer
	// gets to assert, and the server refuses anything else. Forwarded verbatim
	// rather than validated here for that reason: one definition of the word.
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		env["state"] = state
	}

	body, err := json.Marshal(env)
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to serialize push"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pagePushTimeout)
	defer cancel()

	// url.PathEscape on the page: the slug reached us as a path segment from
	// inside the container, and it is about to become a path segment in a URL
	// aimed at an internal route that bypasses the public middleware.
	target := s.ipc.BaseURL + "/api/v1/internal/pages/" + url.PathEscape(page) + "/data"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		writeJSONResponse(w, http.StatusInternalServerError, map[string]string{"error": "failed to build IPC request"})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Internal-Token", s.ipc.Token)

	resp, err := ipcClient.Do(httpReq)
	if err != nil {
		s.logger.Error("page push bridge: IPC request failed", "error", err, "page", page, "panel", panel)
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "page push failed — crewshipd unreachable",
		})
		return
	}
	defer func() { _ = resp.Body.Close() }()

	// Verbatim, deliberately. The server's refusals are the documented ones —
	// "payload does not satisfy metric.v1: …", the produce-authority 403, the
	// 429 floor — and an agent that sees the same sentence a shell script sees
	// can act on the same guide. Bounded at 1 MiB: a push answers with a small
	// envelope, so anything larger is pathological.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		writeJSONResponse(w, http.StatusBadGateway, map[string]string{
			"error": "invalid response from crewshipd",
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// Retry-After travels with the status it belongs to. §10b.3's 429 carries
	// one, and a producer that receives the refusal without the interval has to
	// guess — which is how a rate-limited loop becomes a tighter rate-limited
	// loop.
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		w.Header().Set("Retry-After", ra)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(raw)
}

// readPagePayload reads the body as the panel's payload, bounded and checked
// for JSON syntax only.
//
// The syntax check is not schema validation and must not grow into it: the
// panel's schema is judged once, by pages.ValidatePayload, on the server. What
// it prevents is a category error in the reply. A non-JSON body pasted into the
// envelope breaks the ENVELOPE, and the agent would be told its request was
// malformed when what was malformed was its payload.
func readPagePayload(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, pagePushMaxBodyBytes)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeJSONResponse(w, http.StatusRequestEntityTooLarge, map[string]string{
				"error": fmt.Sprintf("payload exceeds the %d byte panel limit", pages.MaxPayloadBytes),
			})
			return nil, false
		}
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{"error": "could not read request body"})
		return nil, false
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "body is required and carries the panel's payload as JSON",
		})
		return nil, false
	}
	if !json.Valid(payload) {
		writeJSONResponse(w, http.StatusBadRequest, map[string]string{
			"error": "body must be the panel's payload as JSON",
		})
		return nil, false
	}
	return payload, true
}

// splitPagePanelPath reads /pages/{page}/{panel} and refuses anything else.
//
// The refusal of a further slash is load-bearing rather than tidy. Both
// segments are about to be interpolated into a URL aimed at /api/v1/internal/*
// — a prefix that bypasses the public middleware — so "one segment each" is
// what keeps a panel id from addressing a different route.
func splitPagePanelPath(p string) (page, panel string, ok bool) {
	rest, found := strings.CutPrefix(p, "/pages/")
	if !found {
		return "", "", false
	}
	page, panel, found = strings.Cut(rest, "/")
	if !found {
		return "", "", false
	}
	page, panel = strings.TrimSpace(page), strings.TrimSpace(panel)
	if page == "" || panel == "" || strings.Contains(panel, "/") {
		return "", "", false
	}
	// `.` and `..` are path segments, not names. url.PathEscape leaves both
	// untouched, so `/pages/../data` would reach the forwarder as a page called
	// ".." and the upstream URL would normalise to a different internal route
	// before anyone looked at it. A page slug is [a-z0-9_-]; neither of these
	// can be one, so refusing them costs nothing a caller wanted.
	for _, seg := range []string{page, panel} {
		if seg == "." || seg == ".." {
			return "", "", false
		}
	}
	return page, panel, true
}

// ---------------------------------------------------------------------------
// save_page — agent-authored page CREATION, distinct from everything above
// this line. handlePagePush and the /pages/{page}/{panel} route push a
// PAYLOAD onto a panel that already exists on a page a human already built.
// savePage below creates the page's STRUCTURE (metadata + panels) in the
// first place — the gap the setup-agent's own system prompt used to name
// ("a Page YAML is a reviewable draft unless a dedicated Crewship apply
// tool is visibly available"). It wraps
// POST /api/v1/internal/pages/save (internal/api/pages_internal_save.go),
// mirroring savePipeline's shape in pipelines.go: the agent only knows
// about a tool call, and the sidecar is what forwards it to crewshipd with
// the crew/workspace identity that call cannot forge.
//
// Trust model, same as savePipeline's own header: crew + workspace IDs come
// from s.ipc.{WorkspaceID, CrewID}, never from the agent's request. There is
// no test-run step here — a page is a spec, not code with side effects, and
// its own validation (documentFrom, resolveReferences, resolveGates) runs
// server-side on save, same as the human-facing POST /api/v1/pages does.
// ---------------------------------------------------------------------------

// pagesSaveRequest is the agent-facing body for save_page. Panels stays raw
// JSON — the sidecar has no reason to know the shape of a page panel, it
// only has to relay what the agent authored to the endpoint that validates
// it. Client supplies name/description/panels; identity is injected below.
type pagesSaveRequest struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Panels      json.RawMessage `json:"panels"`
	// Crew names the crew this page is built FOR — see pipelinesSaveRequest.Crew
	// for why an agent-supplied crew name is safe here and is not an identity
	// claim.
	Crew string `json:"crew"`
}

// savePage forwards an agent-authored page to the internal save route,
// returning the upstream HTTP status + raw JSON body — a validation failure
// (bad panel schema, unresolved producer, held-by-policy) reaches the
// agent's own retry loop verbatim, exactly like savePipeline's return
// contract.
//
// authorAgentID is the ACTING agent resolved from the per-agent bearer
// token by the calling handler (respondRoutinesMCPToolsCall), matching
// savePipeline's own authorAgentID parameter.
func (s *Server) savePage(ctx context.Context, body pagesSaveRequest, authorAgentID string) (int, []byte) {
	if s.ipc == nil {
		return http.StatusServiceUnavailable, mustJSON(map[string]string{"error": "IPC not configured"})
	}
	if body.Name == "" || len(body.Panels) == 0 {
		return http.StatusBadRequest, mustJSON(map[string]string{"error": "name and panels required"})
	}

	// crew_id stays the unforgeable caller identity from IPC;
	// target_crew_slug is the crew the page is being built FOR, which the API
	// accepts only from the onboarding setup crew (internal_delegated_crew.go).
	saveBody, err := json.Marshal(map[string]any{
		"workspace_id":     s.ipc.WorkspaceID,
		"crew_id":          s.ipc.CrewID,
		"agent_id":         authorAgentID,
		"name":             body.Name,
		"description":      body.Description,
		"panels":           body.Panels,
		"target_crew_slug": body.Crew,
	})
	if err != nil {
		return http.StatusInternalServerError, mustJSON(map[string]string{"error": "marshal save body"})
	}
	saveRes, err := s.ipcRequestJSON(ctx, http.MethodPost, "/api/v1/internal/pages/save", saveBody)
	if err != nil {
		return http.StatusBadGateway, mustJSON(map[string]string{"error": "page-save request failed: " + err.Error()})
	}
	return saveRes.status, saveRes.body
}
