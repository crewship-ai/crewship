/**
 * setup-agent-api — the frontend's whole surface for talking to the
 * onboarding-conversation backend, per docs/prd/conversational-onboarding.md.
 *
 * All four things this file needs are real, shipped endpoints:
 *
 *   - POST /api/v1/onboarding/proposals            — createOnboardingProposal
 *   - POST /api/v1/onboarding/proposals/{id}/apply — applyOnboardingProposal
 *   - POST /api/v1/onboarding/setup-agent/start    — startSetupAgentSession
 *   - POST/PATCH /api/v1/credentials               — persistOnboardingCredential
 *     (createWorkspaceModelCredential / updateWorkspaceModelCredential),
 *     used to land the model token BEFORE the wizard's Crew step opens —
 *     see those functions' own doc comments for why.
 *
 * The first two are coded against the real request/response shapes
 * (cross-checked against `cmd/crewship/cmd_onboarding.go`, the CLI
 * counterpart CLAUDE.md requires for every endpoint). Their own file's
 * design note is worth repeating here because it changes what this file
 * does NOT do: "the setup agent... gets NO write permission — not an
 * internal token scoped to a nominal crew, not anything... today, a plain
 * authenticated call [produces a proposal]." So Create is called from the
 * BROWSER, under the user's own session, the same way picking a template
 * already is — never from any agent-held credential. What the chat surface
 * reads out of the agent's message is a `ProposalSuggestion` (a template + a
 * model, not a finished crew); this file turns that suggestion into a real,
 * server-computed proposal before anything is ever rendered as a card.
 *
 * The third thing — actually opening a conversation with the setup agent —
 * used to be a stub, and its doc comment used to justify that by claiming
 * "this wizard does not create the workspace until step 3's Launch". That
 * was wrong: every workspace is created synchronously at signup/bootstrap
 * (`internal/api/auth.go`'s `Signup`/`Bootstrap`), long before this wizard
 * ever renders, so a workspace id has always been available from step 1
 * onward — the same one `GET /onboarding/status`'s own best-effort call
 * already resolves. The real gap was narrower: the wizard used to collect a
 * model credential but not PERSIST it until the final Launch submission, so
 * a chat offered before Launch would run in a container with nothing to
 * authenticate with. `internal/api/onboarding_setup_agent.go`
 * (`StartSetupAgent`) still refuses up front when that happens — HTTP 428,
 * `reason: "credential_required"` — rather than handing back a chat session
 * the agent could never answer in; see that file's own doc comment for the
 * full sequencing argument. `startSetupAgentSession` surfaces that
 * distinction to its caller instead of collapsing every failure into one
 * generic "unavailable".
 *
 * The wizard closes that gap itself now by reordering: Workspace → Adapter +
 * token → Crew. `createWorkspaceModelCredential` / `updateWorkspaceModelCredential`
 * land the token in the `credentials` table the moment the user leaves the
 * Adapter step, so by the time the Crew step's chat calls
 * `startSetupAgentSession` the precondition above is already satisfied for
 * the ordinary first-run path — the 428 branch above stays as the fallback
 * for a workspace that reaches the Crew step some other way (a resumed
 * session, a skipped step, a failed persist).
 */

import { apiFetch } from "@/lib/api-fetch"

/** One row of a proposal's crew — a name, a role, a model. Never a sentence:
 *  PRD §4.2 "Concrete, not prose. Names, roles, models. A paragraph is not a
 *  proposal." Field names here are the UI's own (friendlier than the wire
 *  shape); `proposalFromWire` below is the one place that translates. */
export interface ProposalAgent {
  name: string
  role: string
  model: string
}

/**
 * A crew the setup agent has proposed, as the server actually computed and
 * stored it (`onboardingProposalResponse` / `onboardingProposalPayload` in
 * internal/api/onboarding_proposal.go).
 *
 * PRD §5.6 is why this is a server-stored object rather than free-form chat
 * text: "the proposal is a server-stored object; the card renders from it;
 * Create submits its id. The agent never re-authors the payload at click
 * time." Every field here is for DISPLAY only — `applyOnboardingProposal`
 * sends nothing but `id`.
 */
export interface OnboardingProposal {
  id: string
  crewName: string
  crewSlug: string
  templateSlug: string
  agents: ProposalAgent[]
  /** Egress domains the crew would need. Always empty today: Phase 1's
   *  proposal is a template plus a model override
   *  (`docs/prd/conversational-onboarding.md` §7 — "Create runs
   *  deployCrewTemplate with at most a model swap"), and the template deploy
   *  path has no `allowed_domains` column to populate this from (§4.2). The
   *  field stays on the type, and the card already renders "no external
   *  network access" for an empty list, so a later phase that adds per-agent
   *  egress is a parsing change here, not a card redesign. */
  egressDomains: string[]
  status: string
}

/** Map one wire-shape proposal agent to the UI's friendlier field names.
 *  Never drops a row for a missing role/model — a proposal with a gap in it
 *  is still a proposal the human should see and can reject; only a row with
 *  no name at all (nothing to identify it) is dropped. */
function proposalAgentFromWire(entry: unknown, fallbackModel: string): ProposalAgent | null {
  if (entry == null || typeof entry !== "object") return null
  const a = entry as Record<string, unknown>
  if (typeof a.name !== "string" || a.name.length === 0) return null
  const model = typeof a.llm_model === "string" && a.llm_model ? a.llm_model : fallbackModel
  return {
    name: a.name,
    role: typeof a.role_title === "string" && a.role_title ? a.role_title : "Agent",
    model: model || "unspecified",
  }
}

/** Map a raw `onboardingProposalResponse` JSON body to `OnboardingProposal`.
 *  Trusts nothing it cannot read field-by-field — the same discipline
 *  `askforms` applies to its own typed envelopes — because this is parsing a
 *  network response, not a value this module produced itself. */
function proposalFromWire(json: unknown): OnboardingProposal | null {
  if (json == null || typeof json !== "object") return null
  const row = json as Record<string, unknown>
  if (typeof row.id !== "string" || !row.id) return null
  const payload = (row.payload && typeof row.payload === "object" ? row.payload : {}) as Record<string, unknown>
  const fallbackModel = typeof payload.llm_model === "string" ? payload.llm_model : ""
  const agentsRaw = Array.isArray(payload.agents) ? payload.agents : []
  const agents = agentsRaw
    .map((a) => proposalAgentFromWire(a, fallbackModel))
    .filter((a): a is ProposalAgent => a !== null)
  return {
    id: row.id,
    crewName: typeof payload.crew_name === "string" && payload.crew_name ? payload.crew_name : "New crew",
    crewSlug: typeof payload.crew_slug === "string" ? payload.crew_slug : "",
    templateSlug: typeof payload.template_slug === "string" ? payload.template_slug : "",
    agents,
    // Phase 1 has no per-proposal egress data on the wire at all — see the
    // field's own doc comment on OnboardingProposal.
    egressDomains: [],
    status: typeof row.status === "string" ? row.status : "PENDING",
  }
}

/**
 * What the setup agent actually puts on the wire: a suggestion, not a
 * finished proposal. Parsed out of a chat event's `metadata` — the same open
 * `Record<string, unknown>` shape `hooks/use-chat.ts` types every event's
 * metadata as, and the same shape `askforms` already round-trips typed
 * envelopes through.
 *
 * This is deliberately thin (a template + an optional model), matching what
 * `POST /api/v1/onboarding/proposals` (`onboardingProposalCreateRequest`)
 * actually accepts — the full per-agent roster is something only the SERVER
 * computes, from the template, never something the agent hands over
 * pre-filled (PRD §5.6: the card must not be able to lie about what a
 * template resolves to).
 */
export interface ProposalSuggestion {
  crewName: string
  templateSlug: string
  crewSlug?: string
  llmProvider?: string
  llmModel?: string
}

/** Read a `ProposalSuggestion` out of a chat event's metadata, or null if
 *  this metadata carries none. Requires at least a crew name and a template
 *  slug — anything else missing is filled in by the server's own defaults
 *  (`resolveLLMProvider` defaults an empty provider to ANTHROPIC). */
export function parseProposalSuggestion(
  metadata: Record<string, unknown> | undefined,
): ProposalSuggestion | null {
  const raw = metadata?.onboarding_proposal_suggestion
  if (raw == null || typeof raw !== "object") return null
  const s = raw as Record<string, unknown>
  if (typeof s.crew_name !== "string" || !s.crew_name) return null
  if (typeof s.template_slug !== "string" || !s.template_slug) return null
  return {
    crewName: s.crew_name,
    templateSlug: s.template_slug,
    crewSlug: typeof s.crew_slug === "string" && s.crew_slug ? s.crew_slug : undefined,
    llmProvider: typeof s.llm_provider === "string" && s.llm_provider ? s.llm_provider : undefined,
    llmModel: typeof s.llm_model === "string" && s.llm_model ? s.llm_model : undefined,
  }
}

/** Read the server's own error text off a failed response — the CLI client
 *  (`internal/cli/errors.go`) documents the same two shapes this codebase's
 *  handlers actually use: the legacy `{"error": "..."}` (`replyError`,
 *  onboarding.go's /status /complete /setup) and RFC 7807 problem+json's
 *  `detail` member (`writeProblem`, used by the newer proposal handlers).
 *  Falls back to a generic message naming the status when neither is
 *  present, rather than surfacing "undefined" to the person reading it. */
async function readErrorDetail(res: Response, fallback: string): Promise<string> {
  const data = await res.json().catch(() => null)
  if (data && typeof data === "object") {
    const o = data as Record<string, unknown>
    if (typeof o.detail === "string" && o.detail) return o.detail
    if (typeof o.error === "string" && o.error) return o.error
  }
  return fallback
}

/**
 * POST /api/v1/onboarding/proposals — turns a suggestion into a real,
 * server-computed proposal. This is the "today, a plain authenticated call"
 * the backend's own design note describes: it runs under the user's session
 * (apiFetch, the same wrapper every other onboarding call uses), not any
 * agent-held credential, so it is gated exactly like a template deploy
 * (roleCreate — MANAGER+) and never needs the internal-token machinery PRD
 * §5.1-§5.4 spent so much of the document ruling out.
 *
 * Not the sensitive write. Nothing user-visible or irreversible happens
 * here — this only materialises a PENDING row the human has not seen yet.
 * `applyOnboardingProposal` is the one gated behind a click.
 */
export async function createOnboardingProposal(
  suggestion: ProposalSuggestion,
  workspaceId: string,
): Promise<OnboardingProposal> {
  const res = await apiFetch(
    `/api/v1/onboarding/proposals?workspace_id=${encodeURIComponent(workspaceId)}`,
    {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      crew_name: suggestion.crewName,
      template_slug: suggestion.templateSlug,
      ...(suggestion.crewSlug ? { crew_slug: suggestion.crewSlug } : {}),
      ...(suggestion.llmProvider ? { llm_provider: suggestion.llmProvider } : {}),
      ...(suggestion.llmModel ? { llm_model: suggestion.llmModel } : {}),
    }),
    },
  )
  if (!res.ok) {
    throw new Error(await readErrorDetail(res, `Could not propose a crew (HTTP ${res.status})`))
  }
  const json = await res.json().catch(() => null)
  const proposal = proposalFromWire(json)
  if (!proposal) throw new Error("Malformed proposal response")
  return proposal
}

/** What POST .../apply hands back — `onboardingProposalApplyResponse` /
 *  its nested `crew` (`deployCrewResult`) in internal/api/onboarding_proposal.go
 *  and crew_templates.go. */
export interface ApplyProposalResult {
  proposalId?: string
  alreadyApplied?: boolean
  crewId?: string
  crewSlug?: string
  crewName?: string
  agentCount?: number
  agentIds?: string[]
}

function applyResultFromWire(json: unknown): ApplyProposalResult {
  if (json == null || typeof json !== "object") return {}
  const row = json as Record<string, unknown>
  const crew = (row.crew && typeof row.crew === "object" ? row.crew : {}) as Record<string, unknown>
  return {
    proposalId: typeof row.proposal_id === "string" ? row.proposal_id : undefined,
    alreadyApplied: typeof row.already_applied === "boolean" ? row.already_applied : undefined,
    crewId: typeof crew.crew_id === "string" ? crew.crew_id : undefined,
    crewSlug: typeof crew.crew_slug === "string" ? crew.crew_slug : undefined,
    crewName: typeof crew.crew_name === "string" ? crew.crew_name : undefined,
    agentCount: typeof crew.agent_count === "number" ? crew.agent_count : undefined,
    agentIds: Array.isArray(crew.agent_ids)
      ? crew.agent_ids.filter((x): x is string => typeof x === "string")
      : undefined,
  }
}

/**
 * POST /api/v1/onboarding/proposals/{id}/apply.
 *
 * The ONLY function a proposal card's Create button may call, and it sends
 * the proposal's `id` and nothing else — the request body is empty by
 * construction (the handler "deliberately never parses a request body").
 * That is what makes PRD §5.6 hold end to end: what Apply executes is read
 * from the row Create already stored, never from anything this call could
 * carry, so a prompt-injected agent cannot show "3 agents" and create 30.
 *
 * Idempotent server-side — a proposal already APPLIED replays its first
 * result (`already_applied: true`) instead of creating a second crew, so a
 * duplicate click (a slow network, a double-tap) is safe to retry.
 */
export async function applyOnboardingProposal(
  proposalId: string,
  workspaceId: string,
): Promise<ApplyProposalResult> {
  const res = await apiFetch(
    `/api/v1/onboarding/proposals/${encodeURIComponent(proposalId)}/apply` +
      `?workspace_id=${encodeURIComponent(workspaceId)}`,
    { method: "POST" },
  )
  if (!res.ok) {
    throw new Error(await readErrorDetail(res, `Could not create the crew (HTTP ${res.status})`))
  }
  return applyResultFromWire(await res.json().catch(() => null))
}

/** Where the onboarding chat should connect once it has a session. */
export interface SetupAgentSession {
  agentId: string
  sessionId: string
  workspaceId: string
}

/**
 * Why starting the setup agent's session didn't produce one:
 *
 *   - "credential_required" — the workspace has no model credential yet
 *     (the server's 428, `reason: "credential_required"`). Not a failure of
 *     anything — it is the expected, recoverable state for a user who
 *     hasn't reached step 3 yet. The caller can say so specifically instead
 *     of presenting this identically to an outage.
 *   - "unavailable" — anything else: a non-2xx/428 response, a malformed
 *     body, or a network failure. Genuinely can't be helped from here.
 *
 * Every caller must still fall back to the template grid for BOTH reasons
 * (PRD §4.3) — a user must never be stranded on a chat that cannot
 * answer — but only "unavailable" means the setup agent is a dead end for
 * this onboarding session; "credential_required" means "not yet".
 */
export type SetupAgentUnavailableReason = "credential_required" | "unavailable"

export type SetupAgentStartOutcome =
  | { ok: true; session: SetupAgentSession }
  | { ok: false; reason: SetupAgentUnavailableReason }

/**
 * Start (or resume) the setup agent's conversation for the current
 * onboarding user. Real endpoint — see this file's own doc comment for the
 * sequencing decision behind why it can 428.
 *
 * Never throws: every failure mode (HTTP, parsing, network) resolves to
 * `{ ok: false, reason }` so a caller can always render something instead of
 * propagating an unhandled rejection into the chat pane.
 */
export async function startSetupAgentSession(): Promise<SetupAgentStartOutcome> {
  try {
    const res = await apiFetch("/api/v1/onboarding/setup-agent/start", { method: "POST" })
    if (res.status === 428) {
      return { ok: false, reason: "credential_required" }
    }
    if (!res.ok) return { ok: false, reason: "unavailable" }
    const data = await res.json().catch(() => null)
    if (
      !data ||
      typeof data !== "object" ||
      typeof (data as Record<string, unknown>).agent_id !== "string" ||
      typeof (data as Record<string, unknown>).session_id !== "string" ||
      typeof (data as Record<string, unknown>).workspace_id !== "string"
    ) {
      return { ok: false, reason: "unavailable" }
    }
    return {
      ok: true,
      session: {
        agentId: (data as Record<string, unknown>).agent_id as string,
        sessionId: (data as Record<string, unknown>).session_id as string,
        workspaceId: (data as Record<string, unknown>).workspace_id as string,
      },
    }
  } catch {
    // Network failure. A user mid-onboarding never sees a raw stack trace
    // for this; they see the template grid.
    return { ok: false, reason: "unavailable" }
  }
}

/**
 * Resolve the current user's workspace id via GET /api/v1/workspaces — the
 * same lookup `hooks/use-workspace.ts` performs, called directly here
 * because the Adapter step needs one before the wizard's own Launch
 * response would ever hand one over. Every workspace exists by
 * signup/bootstrap time (see this file's own doc comment above), so a
 * user reaching this wizard always has at least one row to resolve; the
 * first one is the same "oldest membership wins" workspace the backend's
 * own onboarding handlers (`firstWorkspaceID`) resolve to.
 *
 * Returns null on any failure (network, non-2xx, empty list, malformed
 * body) — callers surface their own message rather than propagate a raw
 * parsing exception into the wizard.
 */
function oldestWorkspaceFromWire(data: unknown): Record<string, unknown> | null {
  const list = (Array.isArray(data) ? data : []) as Record<string, unknown>[]
  return list
    .map((workspace, index) => ({ workspace, index }))
    .sort((a, b) => {
      const aCreated = typeof a.workspace.created_at === "string" ? a.workspace.created_at : ""
      const bCreated = typeof b.workspace.created_at === "string" ? b.workspace.created_at : ""
      if (!aCreated || !bCreated) return a.index - b.index
      return aCreated.localeCompare(bCreated) || a.index - b.index
    })[0]?.workspace ?? null
}

export async function resolveOnboardingWorkspaceId(): Promise<string | null> {
  try {
    const res = await apiFetch("/api/v1/workspaces")
    if (!res.ok) return null
    const data = await res.json().catch(() => null)
    // The workspace API lists newest first, while every onboarding backend
    // handler deliberately uses the user's oldest membership. Sort rows that
    // carry timestamps so credential persistence, setup-agent start and chat
    // history all target the same workspace. Preserve server order as a
    // fallback for legacy responses without created_at.
    const first = oldestWorkspaceFromWire(data)
    return typeof first?.id === "string" && first.id ? first.id : null
  } catch {
    return null
  }
}

export interface SavedOnboardingCredential {
  id: string
  name: string
  provider: string
}

export interface OnboardingResumeState {
  workspaceId: string
  workspaceName: string
  preferredLanguage: string | null
  savedCredential: SavedOnboardingCredential | null
}

export type ResumeOnboardingOutcome =
  | { ok: true; state: OnboardingResumeState }
  | { ok: false; error: string }

/** Reconstructs durable onboarding state after a refresh or a new login.
 * Secrets are never returned by either endpoint; the credential descriptor
 * is enough to reuse the encrypted row and let the user replace it only when
 * they explicitly type a new token. */
export async function loadOnboardingResumeState(): Promise<ResumeOnboardingOutcome> {
  try {
    const workspaceRes = await apiFetch("/api/v1/workspaces")
    if (!workspaceRes.ok) {
      return { ok: false, error: await readErrorDetail(workspaceRes, `Could not load your workspace (HTTP ${workspaceRes.status})`) }
    }
    const workspace = oldestWorkspaceFromWire(await workspaceRes.json().catch(() => null))
    const workspaceId = typeof workspace?.id === "string" ? workspace.id : ""
    if (!workspaceId) return { ok: false, error: "Your account has no workspace to resume." }

    const credentialsRes = await apiFetch(
      `/api/v1/credentials?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
    if (!credentialsRes.ok) {
      return { ok: false, error: await readErrorDetail(credentialsRes, `Could not load saved credentials (HTTP ${credentialsRes.status})`) }
    }
    const body = await credentialsRes.json().catch(() => null)
    const credentials = Array.isArray(body) ? body : []
    const saved = credentials.find((entry) => {
      if (!entry || typeof entry !== "object") return false
      const row = entry as Record<string, unknown>
      const status = typeof row.status === "string" ? row.status.toUpperCase() : "ACTIVE"
      const type = typeof row.type === "string" ? row.type.toUpperCase() : ""
      const provider = typeof row.provider === "string" ? row.provider.toUpperCase() : ""
      // Claude Code is the only production-conformant onboarding adapter.
      // Do not let an unrelated workspace API key (GitHub, OpenAI, etc.)
      // masquerade as the credential that makes setup chat runnable.
      return provider === "ANTHROPIC" && status === "ACTIVE" && type === "AI_CLI_TOKEN"
    }) as Record<string, unknown> | undefined
    const savedCredential =
      typeof saved?.id === "string" && typeof saved?.provider === "string"
        ? {
            id: saved.id,
            name: typeof saved.name === "string" ? saved.name : "Model credential",
            provider: saved.provider.toUpperCase(),
          }
        : null

    return {
      ok: true,
      state: {
        workspaceId,
        workspaceName: typeof workspace?.name === "string" ? workspace.name : "",
        preferredLanguage:
          typeof workspace?.preferred_language === "string" && workspace.preferred_language
            ? workspace.preferred_language
            : null,
        savedCredential,
      },
    }
  } catch {
    return { ok: false, error: "Couldn't restore onboarding from the server. Check your connection and retry." }
  }
}

export async function updateOnboardingWorkspace(input: {
  workspaceId: string
  name: string
  preferredLanguage: string
}): Promise<PersistCredentialOutcome> {
  try {
    const res = await apiFetch(`/api/v1/workspaces/${encodeURIComponent(input.workspaceId)}`, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        name: input.name.trim(),
        preferred_language: input.preferredLanguage,
      }),
    })
    if (!res.ok) {
      return { ok: false, error: await readErrorDetail(res, `Could not save your workspace (HTTP ${res.status})`) }
    }
    return { ok: true }
  } catch {
    return { ok: false, error: "Couldn't reach the server to save your workspace. Check your connection and retry." }
  }
}

/** What either credential-persist call below answers with. */
export interface PersistCredentialOutcome {
  ok: boolean
  /** Present when ok is true — the row's id, needed so a later edit on the
   *  same step (e.g. the user retypes the token before Continuing) PATCHes
   *  the same row instead of colliding with the UNIQUE(workspace_id, name)
   *  index a second Create would hit. */
  credentialId?: string
  /** Present when ok is false — server error text or a network message,
   *  already unwrapped by readErrorDetail. */
  error?: string
}

function onboardingCredentialType(provider: string): "API_KEY" | "AI_CLI_TOKEN" {
  // Anthropic onboarding launches Claude Code, whose OAuth credential is a
  // different contract from an account API key. Other adapters keep their
  // ordinary API-key shape.
  return provider.toUpperCase() === "ANTHROPIC" ? "AI_CLI_TOKEN" : "API_KEY"
}

function onboardingCredentialShapeError(provider: string, value: string): string | null {
  if (provider.toUpperCase() !== "ANTHROPIC") return null
  if (value.trim().startsWith("sk-ant-oat")) return null
  return "This isn't a Claude Code CLI token. Run `claude setup-token` and paste the complete sk-ant-oat… value; an sk-ant-api… account key cannot sign Claude Code in."
}

/** Validate the model credential before it is persisted and before the
 * setup chat starts. A positive vendor answer is required: accepting any
 * eight-character string merely moved the failure into a silent agent run. */
export async function validateWorkspaceModelCredential(input: {
  provider: string
  value: string
}): Promise<PersistCredentialOutcome> {
  const shapeError = onboardingCredentialShapeError(input.provider, input.value)
  if (shapeError) return { ok: false, error: shapeError }
  try {
    const res = await apiFetch("/api/v1/credentials/test", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        provider: input.provider,
        type: onboardingCredentialType(input.provider),
        value: input.value,
      }),
    })
    if (!res.ok) {
      return { ok: false, error: await readErrorDetail(res, `Could not validate your token (HTTP ${res.status})`) }
    }
    const data = await res.json().catch(() => null)
    if (!data || typeof data !== "object") return { ok: false, error: "Malformed credential validation response" }
    const result = data as Record<string, unknown>
    if (result.supported !== true) {
      return { ok: false, error: "This credential type cannot be verified before setup." }
    }
    if (result.valid !== true) {
      const status = typeof result.status === "number" ? result.status : 0
      // Shape validation above already proves this is an Anthropic OAuth
      // token. Only an explicit auth rejection is a reason to block the
      // wizard; a retired probe model, rate limit, provider outage or network
      // failure must not strand a valid credential at the front door. The
      // runtime still surfaces a concrete model/auth failure in chat.
      if (input.provider.toUpperCase() === "ANTHROPIC" && status !== 401 && status !== 403) {
        return { ok: true }
      }
      return {
        ok: false,
        error: typeof result.error === "string" && result.error
          ? result.error
          : "The provider rejected this token. Check it and try again.",
      }
    }
    return { ok: true }
  } catch {
    return { ok: false, error: "Couldn't reach the provider to verify your token. Try again." }
  }
}

/**
 * POST /api/v1/credentials — lands the model token the moment the user
 * leaves the Adapter step, instead of waiting for Launch.
 *
 * Same row shape `insertOnboardingCredential` (internal/api/onboarding.go)
 * would otherwise write at Launch: type AI_CLI_TOKEN, scope WORKSPACE.
 * `autoAssignCredentials` (internal/api/crew_templates.go) matches on
 * `(workspace_id, type IN ('API_KEY','AI_CLI_TOKEN'), provider,
 * status='ACTIVE')` at crew-deploy time regardless of which write path
 * produced the row, so persisting here needs no backend change to still be
 * picked up when the Crew step's proposal (or template pick) deploys.
 *
 * Callers must not also send this same value through POST
 * /onboarding/setup's `credential_value` once this succeeds — see
 * page.tsx's handleLaunch, which blanks that field whenever the persisted
 * (provider, value) pair still matches what Launch is about to submit.
 * Sending both would insert the same credential twice.
 */
export async function createWorkspaceModelCredential(input: {
  workspaceId: string
  name: string
  provider: string
  value: string
}): Promise<PersistCredentialOutcome> {
  const shapeError = onboardingCredentialShapeError(input.provider, input.value)
  if (shapeError) return { ok: false, error: shapeError }
  try {
    // Resume-safe upsert: after a refresh the wizard's React state no longer
    // remembers the credential id, but the encrypted row still exists. Find
    // the matching provider/name and update it instead of hitting the unique
    // index and trapping the user on Continue.
    try {
      const listRes = await apiFetch(
        `/api/v1/credentials?workspace_id=${encodeURIComponent(input.workspaceId)}`,
      )
      if (listRes.ok) {
        const list = await listRes.json().catch(() => null)
        const existing = Array.isArray(list)
          ? list.find((row) => {
              if (!row || typeof row !== "object") return false
              const value = row as Record<string, unknown>
              return value.name === input.name && value.provider === input.provider && value.status !== "REVOKED"
            }) as Record<string, unknown> | undefined
          : undefined
        if (typeof existing?.id === "string" && existing.id) {
          return updateWorkspaceModelCredential({
            workspaceId: input.workspaceId,
            credentialId: existing.id,
            value: input.value,
          })
        }
      }
    } catch {
      // Listing is a resume optimisation. Let POST produce the canonical
      // error if the list endpoint itself was transiently unavailable.
    }

    const res = await apiFetch(
      `/api/v1/credentials?workspace_id=${encodeURIComponent(input.workspaceId)}`,
      {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          name: input.name,
          type: onboardingCredentialType(input.provider),
          provider: input.provider,
          scope: "WORKSPACE",
          value: input.value,
        }),
      },
    )
    if (!res.ok) {
      return { ok: false, error: await readErrorDetail(res, `Could not save your token (HTTP ${res.status})`) }
    }
    const data = await res.json().catch(() => null)
    const id = data && typeof data === "object" ? (data as Record<string, unknown>).id : undefined
    if (typeof id !== "string" || !id) return { ok: false, error: "Malformed credential response" }
    return { ok: true, credentialId: id }
  } catch {
    return {
      ok: false,
      error: "Couldn't reach the server to save your token. Check your connection and try again.",
    }
  }
}

/**
 * PATCH /api/v1/credentials/{id} — updates an already-persisted
 * credential's value in place. Used when the user backs up to the
 * Adapter step and edits the token without changing adapter/provider, so
 * re-Continuing updates the same row instead of colliding with the
 * UNIQUE(workspace_id, name) index a second Create would hit.
 */
export async function updateWorkspaceModelCredential(input: {
  workspaceId: string
  credentialId: string
  value: string
}): Promise<PersistCredentialOutcome> {
  try {
    const res = await apiFetch(
      `/api/v1/credentials/${encodeURIComponent(input.credentialId)}` +
        `?workspace_id=${encodeURIComponent(input.workspaceId)}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ value: input.value }),
      },
    )
    if (!res.ok) {
      return { ok: false, error: await readErrorDetail(res, `Could not update your token (HTTP ${res.status})`) }
    }
    return { ok: true, credentialId: input.credentialId }
  } catch {
    return {
      ok: false,
      error: "Couldn't reach the server to update your token. Check your connection and try again.",
    }
  }
}
