"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import {
  ArrowLeft,
  ArrowRight,
  Check,
  ChevronDown,
  ChevronsUpDown,
  Container,
  ExternalLink,
  Eye,
  EyeOff,
  KeyRound,
  MessageSquare,
  Rocket,
  Sparkles,
  Terminal,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { CrewIcon } from "@/components/ui/crew-icon"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { WizardSteps } from "@/components/ui/wizard-steps"
import { PageError } from "@/components/layout/page-state"
import { CreateSurfaceField, CreateSurfaceToggleRow } from "@/components/layout/create-surface"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelLabel, getModelsForAdapter, isLocalModel } from "@/lib/cli-adapters"
import { ToolchainPicker } from "@/components/features/onboarding/toolchain-picker"
import { CommandSnippet, copyText } from "@/components/features/onboarding/command-snippet"
import { buildOnboardingSetupBody } from "@/lib/onboarding-setup"
import { ADAPTER_TOKEN_GUIDE, ADAPTER_TOKEN_CMD, ADAPTER_CLI_INSTALL } from "@/lib/cli-adapter-brand"
import { LANGUAGES } from "@/lib/languages"
import { cn } from "@/lib/utils"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { serverFetch } from "@/lib/server-base"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewTemplateSlug, HandoffMode } from "@/components/features/onboarding/crew-templates"
import { OnboardingSetupChat } from "@/components/features/onboarding/onboarding-setup-chat"
import { OnboardingCreatedPanel } from "@/components/features/onboarding/onboarding-created-panel"
import { OnboardingProposalSummary } from "@/components/features/onboarding/onboarding-proposal-summary"
import { PairingBlock, type PairStatus } from "@/components/layout/cli-pairing"
import { ChoiceTile } from "@/components/ui/choice-tile"
import { Note } from "@/components/ui/note"
import { TemplatePicker } from "@/components/features/onboarding/template-picker"
import {
  SetupBanner,
  SetupCard,
  SetupCardBody,
  SetupCardFooter,
  SetupCardHeader,
  SetupCardSkeleton,
  SetupFrame,
  ShortcutHint,
} from "@/components/features/onboarding/setup-card"
import {
  createWorkspaceModelCredential,
  loadOnboardingResumeState,
  resolveOnboardingWorkspaceId,
  updateOnboardingWorkspace,
  validateWorkspaceModelCredential,
  updateWorkspaceModelCredential,
} from "@/components/features/onboarding/setup-agent-api"
import type { ApplyProposalResult, OnboardingProposal } from "@/components/features/onboarding/setup-agent-api"

/**
 * First-run setup: Workspace → Model → Crew (Harbor 2, #3409).
 *
 * One card on the ground colour — the dialog anatomy from the component kit
 * (header, WizardSteps, one scrolling body, a footer that never scrolls) —
 * under a lockup that never moves. The form steps are L (800), the crew step
 * XL (960): the Guide chat on the left, what has been built on the right. See
 * components/features/onboarding/setup-card.tsx for the frame.
 *
 * Step order matters here in a way it wouldn't for an ordinary form: the
 * Crew step's default is a chat with a setup agent that runs in a
 * container and needs a model credential to answer at all (see
 * onboarding-setup-chat.tsx and internal/api/onboarding_setup_agent.go).
 * Adapter must come before Crew so that credential exists by the time the
 * chat opens — see `persistAdapterCredential` below for how the token
 * actually lands in the database before step 3 renders.
 */

const STEP_LABELS = ["Workspace", "Model", "Crew"]

/**
 * Map the browser's reported language tag (navigator.language, e.g.
 * "cs-CZ") to one of the entries in our shared LANGUAGES catalog so
 * the picker opens on something familiar. Matches on the leading
 * ISO-639 subtag and prefers exact regional matches (cs-CZ → Czech,
 * pt-BR → Portuguese (Brazil)).
 *
 * Returns the English `name` field, which is what we store verbatim
 * in workspaces.preferred_language and what the orchestrator drops
 * into the system prompt. Falls through to "English" on anything we
 * don't recognise.
 */
function detectDefaultLanguage(): string {
  if (typeof navigator === "undefined") return "English"
  const tag = (navigator.language || "en").toLowerCase()
  // Exact match first (covers pt-BR, zh-TW)
  const exact = LANGUAGES.find((l) => l.code.toLowerCase() === tag)
  if (exact) return exact.name
  // Fall back to leading subtag (covers "en-US" → "en")
  const lead = tag.split(/[-_]/)[0]
  const partial = LANGUAGES.find((l) => l.code.toLowerCase() === lead)
  if (partial) return partial.name
  return "English"
}

/**
 * Look up an agent's slug from its id.
 *
 * POST /onboarding/setup answers with `agent_id`, and chat is addressed by
 * slug (`/chat/<agentSlug>` — the slug is parsed straight out of
 * window.location.pathname, see chat-page-client.tsx). One GET bridges the
 * two. It is on the critical path of the wizard's last click, so every
 * failure mode returns null and the caller falls back to a page that exists
 * rather than blocking completion on a lookup.
 */
async function fetchAgentSlug(agentId: string, workspaceId: string): Promise<string | null> {
  try {
    const res = await serverFetch(
      `/api/v1/agents/${encodeURIComponent(agentId)}?workspace_id=${encodeURIComponent(workspaceId)}`,
    )
    if (!res.ok) return null
    const agent = await res.json()
    return typeof agent?.slug === "string" && agent.slug ? agent.slug : null
  } catch {
    return null
  }
}

type Step = 1 | 2 | 3

export default function OnboardingPage() {
  const router = useRouter()
  const [step, setStep] = useState<Step>(1)
  const [checking, setChecking] = useState(true)
  const [bootstrapError, setBootstrapError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [workspaceName, setWorkspaceName] = useState("")
  const [onboardingWorkspaceId, setOnboardingWorkspaceId] = useState<string | null>(null)
  const [persistingWorkspace, setPersistingWorkspace] = useState(false)
  const [language, setLanguage] = useState<string>("English")
  const [crewSlug, setCrewSlug] = useState<CrewTemplateSlug | null>(null)
  // Step 3's two paths. "chat" is the new default — a conversation with the
  // setup agent that ends in a proposal (docs/prd/conversational-onboarding.md
  // §4). "template" is the escape hatch (§4.3): a user who already knows what
  // they want can still pick a template directly and move on, and it is also
  // the automatic landing spot when the setup agent turns out to be
  // unavailable (see chatUnavailable below).
  const [crewMode, setCrewMode] = useState<"chat" | "template">("chat")
  const [chatUnavailable, setChatUnavailable] = useState(false)
  // Set only for the "credential_required" flavour of unavailability — the
  // workspace has no model token yet. In the ordinary first-run path this
  // cannot happen any more: step 2 (Adapter) persists the token via
  // persistAdapterCredential before step 3 ever opens. It stays possible for
  // a workspace that reaches step 3 some other way (a resumed session, a
  // failed persist the user clicked past, a local-model pick that later
  // switches). Unlike a genuine outage this is expected and recoverable, so
  // — unlike chatUnavailable — it does NOT hide the "talk to the setup agent
  // instead" link; the user can go back to step 2, fix the token, and retry.
  const [chatNeedsCredential, setChatNeedsCredential] = useState(false)
  // Set once a proposal from the setup agent has actually been applied
  // (POST /onboarding/proposals/{id}/apply succeeded) — the crew is real at
  // that point, same as picking a template, just not through crewSlug (which
  // only names a *builtin* template). Carries only what the card already
  // showed the user, never anything re-derived after the click.
  // A LIST, not a slot. The Guide can propose a second crew in the same
  // conversation — people really do ask for "and another one that watches X"
  // — and each Create applies its own proposal, so more than one crew is
  // genuinely created. This used to be a single `appliedProposal`, so crew #2
  // overwrote crew #1 the moment its suggestion arrived: both crews existed
  // in the database, but the panel only ever named the newest, and the person
  // reasonably concluded the product could not create more than one.
  const [createdCrews, setCreatedCrews] = useState<
    Array<{ id: string; proposal: OnboardingProposal; result: ApplyProposalResult }>
  >([])
  // The proposal currently on the card and NOT yet created. Still a single
  // slot, and correctly so: the Guide revises one proposal at a time, and a
  // revision should replace its predecessor rather than stack up.
  const [preparedProposal, setPreparedProposal] = useState<OnboardingProposal | null>(null)
  // Crews that already exist in the workspace, read back by the "Built so
  // far" panel. Distinct from createdCrews, which only ever holds what THIS
  // page's Create clicks made: a reload after Create emptied it, Launch went
  // disabled, and the person had a real crew and no way to finish setup.
  const [existingCrewCount, setExistingCrewCount] = useState(0)
  // Skip is one click away from Continue and irreversible in the sense that
  // matters — the wizard never opens again — so it asks first.
  const [skipDialogOpen, setSkipDialogOpen] = useState(false)
  // Set when Launch has succeeded. The wizard used to redirect straight into
  // the first agent's chat, so the last thing a person saw of their own setup
  // was a text box — nothing ever told them what had actually been built, and
  // with more than one crew there is no single chat that represents the work.
  // Holding here and showing the receipt is the difference between "something
  // happened" and "here is what you now have".
  const [launchSummary, setLaunchSummary] = useState<{ agentSlug: string | null } | null>(null)
  // Browser, not CLI. The old default was "cli", reasoning that Claude Code
  // users almost always have a local CLI already — true of people who
  // already run Crewship, not of the person this screen exists for, who is
  // installing it for the first time and may have no CLI at all.
  const [mode, setMode] = useState<HandoffMode>("browser")
  const [adapter, setAdapter] = useState<string>("CLAUDE_CODE")
  const [model, setModel] = useState<string>("")
  const [apiKey, setApiKey] = useState("")
  // The token field is type=password by default (it is a secret), but a
  // person pasting a 100-character string they cannot see has no way to tell
  // a truncated paste from a good one. Reveal is opt-in and per session.
  const [showApiKey, setShowApiKey] = useState(false)
  // Tracks the credential row persistAdapterCredential has already written
  // for THIS token, so leaving step 2 a second time (Back, edit, Continue
  // again) updates that row instead of colliding with the
  // UNIQUE(workspace_id, name) index a second Create would hit, and so
  // handleLaunch knows not to send the same value again — see
  // persistAdapterCredential's own comment below.
  const [persistedCredential, setPersistedCredential] = useState<{
    id: string
    provider: string
    // null means the encrypted row came from the server after a reload. The
    // plaintext is intentionally unrecoverable and an empty input means
    // "reuse it", not "delete it".
    apiKey: string | null
  } | null>(null)
  const [persistingCredential, setPersistingCredential] = useState(false)
  // Crash-reporting consent. Seeded from the server's current state (see
  // the /api/v1/system/telemetry effect below) so the checkbox reflects
  // the build's default — prerelease/dev servers boot default-on, stable
  // servers default-off. The user's explicit answer rides the setup
  // submission as `telemetry_opt_in` and is sticky server-side.
  const [telemetryOptIn, setTelemetryOptIn] = useState(false)
  const [pairRemainingSec, setPairRemainingSec] = useState<number | null>(null)

  const [pairCode, setPairCode] = useState<string | null>(null)
  const [pairExpiresAt, setPairExpiresAt] = useState<string | null>(null)
  // "starting" is a distinct in-flight state so the auto-start effect
  // doesn't race a manual retry click: the effect only fires when
  // status === "idle", and startPairing flips to "starting" before
  // any await. Without this, clicking Retry after an expiry could
  // mint two codes — UI keeps the second one but the first stays
  // valid server-side until its 10-min TTL elapses.
  // Whether the credential block is expanded. CLI mode collapses it behind
  // "Or add it now"; browser mode ignores this and always shows it.
  // (showCredential is gone: the credential block is no longer collapsible.
  // Hiding it was what took the model picker away in CLI mode.)
  const [pairStatus, setPairStatus] = useState<PairStatus>("idle")
  // Whether the workspace holds a model token for the agents yet — a
  // different credential from the CLI token pairing mints. See the poll below.
  const [tokenDelivered, setTokenDelivered] = useState(false)
  const [pairCopied, setPairCopied] = useState(false)
  // Whether a container runtime is INSTALLED (advisory, step 1 copy).
  const [runtimeReady, setRuntimeReady] = useState<boolean | null>(null)
  // Whether THIS SERVER is actually driving one. The distinction decides
  // whether step 3 can work at all: step 3 opens a chat with an agent that
  // runs in a container, and a host with Docker installed but a crewshipd
  // started with --no-docker reports available=true and can start nothing.
  // null = the probe has not answered yet.
  const [runtimeInUse, setRuntimeInUse] = useState<boolean | null>(null)
  const [runtimeChecking, setRuntimeChecking] = useState(false)
  // Step 2 with a token already saved on the server: the field reads "Token
  // saved" until the person chooses Replace (the secret never comes back).
  const [replacingSaved, setReplacingSaved] = useState(false)
  // Phone only: "Built so far" folds above the chat so the chat keeps the room.
  const [builtOpen, setBuiltOpen] = useState(false)

  // Status and resume are one fail-closed bootstrap. The old gate translated
  // every 401/500/network failure into {completed:false}; a stale login thus
  // looked exactly like a brand-new account and asked the user to recreate a
  // workspace and credential that still existed. apiFetch refreshes auth,
  // and any remaining failure gets an explicit Retry screen instead of a
  // destructive-looking fresh wizard.
  const bootstrapOnboarding = useCallback(async () => {
    setChecking(true)
    setBootstrapError(null)
    try {
      const statusRes = await apiFetch("/api/v1/onboarding/status")
      if (!statusRes.ok) {
        setBootstrapError(`Could not verify onboarding status (HTTP ${statusRes.status}).`)
        setChecking(false)
        return
      }
      const status = await statusRes.json().catch(() => null)
      if (status && typeof status === "object" && (status as { completed?: unknown }).completed === true) {
        router.replace("/")
        return
      }

      const resumed = await loadOnboardingResumeState()
      if (!resumed.ok) {
        setBootstrapError(resumed.error)
        setChecking(false)
        return
      }
      const snapshot = resumed.state
      setOnboardingWorkspaceId(snapshot.workspaceId)
      setWorkspaceName(snapshot.workspaceName)
      if (snapshot.preferredLanguage) {
        setLanguage(snapshot.preferredLanguage)
        // preferred_language is written only when step 1 successfully
        // Continues. It doubles as a durable checkpoint without another
        // migration or browser storage, so a re-login resumes at Adapter.
        setStep(2)
      }

      if (snapshot.savedCredential) {
        const provider = snapshot.savedCredential.provider.toUpperCase()
        const matchingAdapter = CLI_ADAPTER_KEYS.find(
          (key) =>
            CLI_ADAPTERS[key].provider.toUpperCase() === provider &&
            CLI_ADAPTERS[key].status === "production",
        )
        if (matchingAdapter) {
          setAdapter(matchingAdapter)
          setModel(CLI_ADAPTERS[matchingAdapter].defaultModel)
          setPersistedCredential({
            id: snapshot.savedCredential.id,
            provider,
            apiKey: null,
          })
          setTokenDelivered(true)
          // Workspace and credential are already durable. Resume at the
          // first unfinished decision instead of demanding both again.
          setStep(3)
        }
      }
      setChecking(false)
    } catch {
      setBootstrapError("Couldn't restore onboarding from the server. Check your connection and retry.")
      setChecking(false)
    }
  }, [router])

  useEffect(() => {
    void bootstrapOnboarding()
  }, [bootstrapOnboarding])

  // Extracted so the step-2 gate can offer a re-check: the probe used to run
  // once on mount, which left anyone who started Docker mid-wizard stuck
  // behind a hard block until they reloaded the page.
  const checkRuntime = useCallback(async () => {
    setRuntimeChecking(true)
    try {
      const r = await serverFetch("/api/v1/system/runtime")
      const d = r.ok ? await r.json() : { available: false, in_use: false }
      setRuntimeReady(Boolean(d.available))
      setRuntimeInUse(Boolean(d.in_use))
    } catch {
      setRuntimeReady(false)
      setRuntimeInUse(false)
    } finally {
      setRuntimeChecking(false)
    }
  }, [])

  useEffect(() => {
    void checkRuntime()
  }, [checkRuntime])

  // Seed the telemetry consent checkbox from the server's current state:
  // prerelease/dev builds boot with crash reporting defaulted on, stable
  // builds default off (internal/crashreport.DefaultOptIn). On any fetch
  // failure the checkbox stays unticked — the privacy-preserving default.
  useEffect(() => {
     
    serverFetch("/api/v1/system/telemetry")
      .then((r) => (r.ok ? r.json() : { enabled: false }))
      .then((d) => setTelemetryOptIn(Boolean(d.enabled)))
      .catch(() => undefined)
  }, [])

  // Seed the language picker from the browser locale so a Czech
  // visitor gets "Čeština" preselected and English speakers see
  // "English" without having to touch the picker. Effect runs once
  // on mount; if the user overrides we never re-detect.
  useEffect(() => {
    setLanguage(detectDefaultLanguage())
  }, [])

  useEffect(() => {
    const cfg = CLI_ADAPTERS[adapter]
    if (cfg && !model) setModel(cfg.defaultModel)
  }, [adapter, model])

  // Pairing poll loop
  useEffect(() => {
    if (mode !== "cli" || step !== 2 || !pairCode || pairStatus !== "pending") return
    const interval = setInterval(async () => {
      try {
        // eslint-disable-next-line no-restricted-syntax -- CLI pairing poll during onboarding; auth endpoint, raw fetch by design
        const res = await fetch(`/api/v1/auth/pair/poll?code=${encodeURIComponent(pairCode)}`)
        if (!res.ok) return
        const data = await res.json()
        if (data.status === "consumed") setPairStatus("consumed")
        else if (data.status === "expired") setPairStatus("expired")
      } catch {
        // network blip — keep polling
      }
    }, 2000)
    return () => clearInterval(interval)
  }, [mode, step, pairCode, pairStatus])

  // Model-token poll. Pairing signs the TERMINAL in; it does not give the
  // agents a key, and those are two different credentials that both get
  // called "token". `crewship login --pair` offers to land the model token
  // right after it pairs, so once the pair is consumed this watches for it to
  // show up and the step can state what is actually true instead of promising
  // a repair that does not exist.
  //
  // The order is the whole reason this matters: autoAssignCredentials links
  // workspace credentials to agents when the crew is DEPLOYED. A token that
  // arrives after Launch reaches none of them, and `crewship setup` answers
  // 409 once onboarding is complete — so "you can add it later" was false.
  useEffect(() => {
    if (mode !== "cli" || step !== 2 || pairStatus !== "consumed" || tokenDelivered) return
    let cancelled = false
    const check = async () => {
      try {
        // Use the same workspace-scoped, typed credential lookup as resume.
        // A token in a different workspace (or an account API key) cannot
        // make this crew runnable. Retain the descriptor so Continue can
        // reuse the encrypted token without asking the user to paste it again.
        const resumed = await loadOnboardingResumeState()
        if (!resumed.ok || cancelled) return
        const { workspaceId, savedCredential } = resumed.state
        const provider = CLI_ADAPTERS[adapter]?.provider.toUpperCase()
        if (workspaceId !== onboardingWorkspaceId || !savedCredential || savedCredential.provider.toUpperCase() !== provider) return
        setPersistedCredential({ id: savedCredential.id, provider, apiKey: null })
        setTokenDelivered(true)
      } catch {
        // network blip — the next tick retries
      }
    }
    void check()
    const interval = setInterval(check, 2500)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [mode, step, pairStatus, tokenDelivered, adapter, onboardingWorkspaceId])

  // Live countdown for the pair-code expiry. Updates every second
  // while the code is pending so the user can see at a glance how
  // long they have before they need a fresh code. We refresh from
  // pairExpiresAt rather than counting locally so a brief tab-switch
  // doesn't desync the countdown.
  useEffect(() => {
    if (!pairExpiresAt || pairStatus !== "pending") {
      setPairRemainingSec(null)
      return
    }
    const tick = () => {
      const remaining = Math.max(0, Math.round((new Date(pairExpiresAt).getTime() - Date.now()) / 1000))
      setPairRemainingSec(remaining)
      if (remaining === 0) setPairStatus("expired")
    }
    tick()
    const interval = setInterval(tick, 1000)
    return () => clearInterval(interval)
  }, [pairExpiresAt, pairStatus])

  const startPairing = useCallback(async () => {
    setError(null)
    // Flip to "starting" BEFORE the await so the auto-start effect
    // (which keys off status === "idle") can't fire in parallel and
    // mint a second code. Status transitions:
    //   idle → starting → pending (success) | failed (error)
    setPairStatus("starting")
    try {
       
      const res = await serverFetch("/api/v1/auth/pair/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ adapter_hint: adapter }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error ?? "Could not start pairing")
        setPairStatus("failed")
        return
      }
      const data = await res.json()
      setPairCode(data.code)
      setPairExpiresAt(data.expires_at)
      setPairStatus("pending")
    } catch {
      setError("Network error starting pairing")
      setPairStatus("failed")
    }
  }, [adapter])

  useEffect(() => {
    // Auto-start pairing on first arrival at step 2 (CLI mode). Don't
    // retry on failure — the "failed" status surfaces a manual retry
    // button instead, so we don't hammer the server in a hot loop if
    // /pair/start is consistently rejecting.
    if (mode === "cli" && step === 2 && !pairCode && pairStatus === "idle") {
      void startPairing()
    }
  }, [mode, step, pairCode, pairStatus, startPairing])

  /**
   * The full CLI invocation the user should paste — code AND server.
   * Without --server, the CLI defaults to http://localhost:8080,
   * which only works for an operator who happens to be running the
   * server on the same machine. The browser already knows where the
   * server lives (window.location.origin), so we encode it directly
   * into the snippet and the user doesn't have to figure it out.
   *
   * Skips localhost-style URLs since the CLI already defaults there
   * — a shorter snippet on a developer's local machine reads more
   * cleanly than `--server=http://localhost:8080`.
   */
  const pairCommand = (() => {
    if (!pairCode) return ""
    let server = ""
    if (typeof window !== "undefined") {
      const origin = window.location.origin
      const isLocalDefault =
        origin === "http://localhost:8080" || origin === "http://127.0.0.1:8080"
      if (!isLocalDefault) {
        server = ` --server=${origin}`
      }
    }
    return `crewship login --pair --code=${pairCode}${server}`
  })()

  const copyPairCmd = useCallback(() => {
    if (!pairCommand) return
    copyText(pairCommand, () => {
      setPairCopied(true)
      setTimeout(() => setPairCopied(false), 1500)
    })
  }, [pairCommand])

  const selectedProvider = (CLI_ADAPTERS[adapter]?.provider || "ANTHROPIC").toUpperCase()

  /**
   * A client-side read of what was pasted — shape only, never validity. The
   * real check is validateWorkspaceModelCredential on Continue; this exists
   * so the one predictable mistake (a console API key) is named while the
   * person is still looking at the field.
   */
  const tokenHint = ((): { tone: "warn" | "ok"; text: string } | null => {
    const v = apiKey.trim()
    if (!v || adapter !== "CLAUDE_CODE") return null
    if (v.startsWith("sk-ant-api")) {
      return {
        tone: "warn",
        text: "This is an account API key from the Anthropic console. Crewship needs the CLI token that `claude setup-token` prints — paste that instead.",
      }
    }
    if (v.startsWith("sk-ant-oat")) {
      return { tone: "ok", text: "Looks like a Claude Code CLI token. It is verified with Anthropic when you continue." }
    }
    return null
  })()
  const savedCredentialSelected = Boolean(
    persistedCredential &&
      persistedCredential.provider === selectedProvider &&
      persistedCredential.apiKey === null &&
      apiKey.trim() === "",
  )

  /**
   * Step 2 validation — the model token is required in BOTH handoff modes,
   * because it is a fact about the AGENTS, not about how the human drives
   * Crewship. Agents run in containers and need a provider credential to
   * call their model. Pairing mints a CLI token for the operator's
   * terminal; the two are unrelated and only share the word "token".
   *
   * This gate has been wrong in both directions. It first required
   * `keyOK && pairStatus === "consumed"` — correct about the key, but it
   * blocked Continue with no explanation, which read as a dead end. The fix
   * for that was to say WHY; instead the key requirement was dropped, and
   * that produced something worse: a crew of four agents with zero
   * credentials, unable to answer and unrepairable — `crewship setup`
   * answers 409 once a crew exists, and a credential created afterwards is
   * never linked, because autoAssignCredentials runs at deploy time.
   *
   * So: the key is required, and pairing is NOT. Pairing is an optional
   * convenience — a person installing Crewship for the first time does not
   * have the CLI yet, and must not be sent to a GitHub release page to
   * finish signing up. They can pair whenever they like, afterwards.
   *
   * Exception (#944): local (ollama/…) models talk to the operator's own
   * endpoint and need no provider credential, so the key becomes optional.
   */
  const canContinue = () => {
    if (step === 1) return workspaceName.trim().length >= 2
    if (step === 2) {
      // The onboarding image is conformance-tested with Claude Code only.
      // Other adapters remain available in the product as explicitly
      // experimental choices, but letting a first-run user continue would
      // create a crew whose default image may not contain the selected CLI.
      // Fail at the choice, with an explanation, rather than much later as
      // an exit-127 chat that looks like the app ignored them.
      const adapterReady = CLI_ADAPTERS[adapter]?.status === "production"
      // Step 3 opens a chat with an agent that runs inside a container. With
      // no runtime driving that, the wizard used to let the user through and
      // then answer their first message with two stacked errors naming an
      // internal component — after they had already committed to the step.
      // Same rule as the adapter check above: fail at the choice, with an
      // explanation, not later as something that looks like the app ignoring
      // them. `null` (probe still in flight) blocks too, and the panel says
      // "Checking…" so it never reads as a silent refusal.
      const runtimeOk = runtimeInUse === true
      return runtimeOk && adapterReady && (savedCredentialSelected || apiKey.trim().length >= 8 || isLocalModel(model))
    }
    if (step === 3) {
      return crewMode === "template" ? crewSlug !== null : createdCrews.length > 0 || existingCrewCount > 0
    }
    return false
  }

  /**
   * Why the primary button is disabled, in one sentence, shown beside it.
   * A disabled button with no reason is the wizard's own dead end: every
   * gate here is legitimate, but a person who cannot see what it wants
   * from them assumes the product is broken and leaves.
   */
  const blockingReason = (): string | null => {
    if (canContinue()) return null
    if (step === 1) return "Give your workspace a name to continue."
    if (step === 2) {
      if (runtimeInUse !== true) return "Waiting for a container runtime."
      if (CLI_ADAPTERS[adapter]?.status !== "production") return "Choose Claude Code to finish setup."
      return "Paste your CLI token to continue."
    }
    if (step === 3) {
      return crewMode === "template" ? "Pick a template to launch." : "Create a crew in the chat first, then launch."
    }
    return null
  }

  /**
   * Land the Adapter step's model token in the database BEFORE step 3
   * (Crew) opens, so its default chat with the setup agent doesn't 428 —
   * see this file's own doc comment above and setup-agent-api.ts's for the
   * full sequencing argument. Called from the Continue button when leaving
   * step 2; returns false (and sets `error`) when the caller should NOT
   * advance, true otherwise (including the no-op cases: a local model
   * needs no credential, and an unchanged already-persisted token needs no
   * second write).
   *
   * Idempotent across repeat visits to step 2: `persistedCredential` tracks
   * the (provider, value) pair this session has already written, so
   * editing the token and Continuing again PATCHes that same row instead
   * of colliding with the UNIQUE(workspace_id, name) index a second Create
   * would hit. Switching adapter/provider after a persist leaves the old
   * row alone and creates a new one for the new provider — deploy-time
   * autoAssignCredentials matches per-agent provider, so an unused leftover
   * row for an abandoned provider is harmless.
   */
  const persistAdapterCredential = useCallback(async (): Promise<boolean> => {
    if (isLocalModel(model)) return true
    const trimmed = apiKey.trim()
    if (trimmed.length < 8) return savedCredentialSelected
    const adapterCfg = CLI_ADAPTERS[adapter]
    const provider = (adapterCfg?.provider || "ANTHROPIC").toUpperCase()
    if (persistedCredential && persistedCredential.provider === provider && persistedCredential.apiKey === apiKey) {
      return true // nothing changed since the last successful persist
    }
    setPersistingCredential(true)
    setError(null)
    try {
      const workspaceId = onboardingWorkspaceId ?? await resolveOnboardingWorkspaceId()
      if (!workspaceId) {
        setError("Could not find your workspace. Refresh the page and try again.")
        return false
      }
      const validation = await validateWorkspaceModelCredential({ provider, value: apiKey })
      if (!validation.ok) {
        setError(validation.error ?? "The provider could not verify your token. Check it and try again.")
        return false
      }
      const outcome =
        persistedCredential && persistedCredential.provider === provider
          ? await updateWorkspaceModelCredential({
              workspaceId,
              credentialId: persistedCredential.id,
              value: apiKey,
            })
          : await createWorkspaceModelCredential({
              workspaceId,
              name: adapterCfg?.envVar || "API Key",
              provider,
              value: apiKey,
            })
      if (!outcome.ok || !outcome.credentialId) {
        setError(outcome.error ?? "Could not save your token. Try again.")
        return false
      }
      setPersistedCredential({ id: outcome.credentialId, provider, apiKey })
      return true
    } finally {
      setPersistingCredential(false)
    }
  }, [model, apiKey, adapter, persistedCredential, onboardingWorkspaceId, savedCredentialSelected])

  /** Continue persists each completed choice before advancing. A reload can
   * therefore reconstruct the real workspace and reuse its encrypted token
   * instead of replaying a blank in-memory wizard. */
  const handleContinue = useCallback(async () => {
    if (step === 1) {
      setPersistingWorkspace(true)
      setError(null)
      try {
        const workspaceId = onboardingWorkspaceId ?? await resolveOnboardingWorkspaceId()
        if (!workspaceId) {
          setError("Could not find your workspace. Refresh the page and try again.")
          return
        }
        const saved = await updateOnboardingWorkspace({
          workspaceId,
          name: workspaceName,
          preferredLanguage: language,
        })
        if (!saved.ok) {
          setError(saved.error ?? "Could not save your workspace. Try again.")
          return
        }
        setOnboardingWorkspaceId(workspaceId)
      } finally {
        setPersistingWorkspace(false)
      }
    }
    if (step === 2) {
      const ok = await persistAdapterCredential()
      if (!ok) return
    }
    setStep((s) => (s < 3 ? ((s + 1) as Step) : s))
  }, [step, persistAdapterCredential, onboardingWorkspaceId, workspaceName, language])

  /**
   * The setup agent couldn't be reached. Fall back to the template grid
   * automatically rather than leave the pane stuck on a spinner (PRD §4.3's
   * fallback) — but WHY matters, per setup-agent-api.ts's
   * SetupAgentUnavailableReason doc comment:
   *
   *   - "credential_required": expected, not a failure, though in the
   *     ordinary first-run path it should no longer happen — step 2
   *     already persisted the token before step 3 opened. Still handled
   *     the same way for a workspace that reaches step 3 some other way
   *     (a resumed session, a failed persist the user clicked past). This
   *     is NOT treated as `chatUnavailable` — that flag hides the "talk to
   *     the setup agent instead" link, and here it should stay: nothing
   *     about the setup agent is actually broken, so a user who switches
   *     to chat again later gets a fresh, identical attempt, not a link
   *     back to something known to be dead.
   *   - "unavailable": a real failure (outage, malformed response, network).
   *     `chatUnavailable` hides the return link so the template pane
   *     doesn't offer a way back to something that will just fail again.
   */
  const handleSetupAgentUnavailable = useCallback((reason: "credential_required" | "unavailable") => {
    setCrewMode("template")
    if (reason === "credential_required") {
      setChatNeedsCredential(true)
      return
    }
    setChatUnavailable(true)
  }, [])

  /**
   * A proposal was actually applied (PRD §5.6: the card and the mutation
   * come from the same server-stored object, and this is the ONLY place that
   * result reaches page state). `result` is deliberately not trusted beyond
   * what it is — an id-bearing acknowledgement — and `crewName` comes from
   * the proposal the human actually read, not from anything re-derived
   * after the click.
   */
  const handleProposalApplied = useCallback((result: ApplyProposalResult, proposal: OnboardingProposal) => {
    const named = { ...proposal, crewName: result.crewName ?? proposal.crewName }
    setCreatedCrews((prev) =>
      // Idempotent on proposal id: Apply itself replays rather than creating a
      // second crew, so a double-click must not add a second row here either.
      prev.some((c) => c.id === proposal.id)
        ? prev
        : [...prev, { id: proposal.id, proposal: named, result }],
    )
    // The card for this proposal is done; clear the pending slot so the panel
    // shows it under "created" instead of leaving it queued for a Create that
    // already happened.
    setPreparedProposal(null)
  }, [])

  async function handleLaunch() {
    setSubmitting(true)
    setError(null)
    try {
      // Resumed after a reload with crews already built, and no applied
      // proposal id left in memory to hand to /setup. Sending /setup without
      // one would deploy a second, blank crew; /complete records completion
      // and nothing else, which is exactly what remains to be done.
      if (crewMode === "chat" && createdCrews.length === 0 && existingCrewCount > 0) {
        const res = await apiFetch("/api/v1/onboarding/complete", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ skipped: false }),
        })
        if (!res.ok && res.status !== 409) {
          const data = await res.json().catch(() => ({}))
          setError(data.error ?? `Could not finish setup (HTTP ${res.status}). Try again.`)
          setSubmitting(false)
          return
        }
        try {
          window.localStorage.setItem("crewship.justOnboarded", "1")
          window.localStorage.removeItem("crewship.firstAgentId")
          window.localStorage.removeItem("crewship.firstAgentSlug")
        } catch {
          // localStorage unavailable — the dashboard simply skips the banner.
        }
        setLaunchSummary({ agentSlug: null })
        return
      }
      const adapterCfg = CLI_ADAPTERS[adapter]
      // A crew from the setup agent's conversation isn't a builtin template,
      // so crewSlug (which only ever names one) has nothing to pass here.
      //
      // Reordering the wizard to Workspace → Adapter → Crew made this branch
      // common instead of rare: the chat now opens with a credential already
      // in place (persistAdapterCredential, step 2), so most first-run users
      // reach Launch via an applied proposal, not a picked template.
      //
      // When a proposal was applied, send its id via applied_proposal_id and
      // nothing else crew-shaped — the server's applied_proposal_id branch
      // persists prefs/telemetry/completion and returns the crew the
      // proposal already created, WITHOUT deploying a second one. This
      // replaces the "blank" signal that used to be sent here, which made
      // POST /onboarding/setup run the single-agent deploy path a second
      // time and left the user with two crews from one onboarding.
      const body: Record<string, unknown> = buildOnboardingSetupBody({
        workspaceName,
        language,
        crewSlug,
        // One id, though several crews may exist: the server uses it only
        // to resolve which crew the post-launch redirect lands on (see
        // setupFromAppliedProposal — it deploys nothing, every crew was
        // already created by its own Create click). The most recent is the
        // one the person was last looking at.
        appliedProposalId: createdCrews.at(-1)?.id,
        adapter,
        adapterLabel: adapterCfg?.label,
        provider: adapterCfg?.provider,
        envVar: adapterCfg?.envVar,
        model,
        // Already persisted at step 2 (persistAdapterCredential) when the
        // provider/value haven't changed since — sending it again here
        // would insert a second credential row for the same value
        // (insertOnboardingCredential has no idempotency of its own). Only
        // fall back to sending it fresh when something about the adapter
        // choice changed after the early persist (see canContinue/
        // persistAdapterCredential above).
        apiKey:
          persistedCredential &&
          persistedCredential.provider === selectedProvider &&
          ((persistedCredential.apiKey === null && apiKey.trim() === "") ||
            persistedCredential.apiKey === apiKey)
            ? ""
            : apiKey,
        pairingMode: mode === "cli",
        telemetryOptIn,
      })

      const res = await apiFetch("/api/v1/onboarding/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      // 409 = onboarding already completed (another tab raced through).
      // No point showing an error — we just bounce them onto the
      // dashboard so they're not stuck on a wizard with no exit.
      if (res.status === 409) {
        router.push("/")
        return
      }
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        // Surface the server's actual message — usually a validation
        // error with concrete cause (e.g. "Unknown crew template").
        // Generic catch-all only fires when the response had no body.
        setError(data.error ?? `Setup failed (HTTP ${res.status}). Try again or contact your admin.`)
        setSubmitting(false)
        return
      }
      const data = await res.json()
      // Chat lives at /chat/<agentSlug>, and setup answers with an id, so the
      // slug is resolved here — once — and shared by the redirect below and
      // the dashboard's welcome checklist (which reads the breadcrumb).
      const firstAgentSlug = data.agent_id && data.workspace_id
        ? await fetchAgentSlug(String(data.agent_id), String(data.workspace_id))
        : null
      // Drop a "just onboarded" breadcrumb so the dashboard knows to
      // render the welcome checklist on the user's next mount. Both
      // exit paths set it because the chat-redirect user may bounce
      // straight to / via the sidebar before they've seen the chat,
      // and we still want them to land on the checklist there. The
      // banner has its own dismissed-flag check so a returning user
      // who already opted out doesn't re-see it.
      try {
        if (typeof window !== "undefined") {
          window.localStorage.setItem("crewship.justOnboarded", "1")
          if (data.agent_id) {
            window.localStorage.setItem("crewship.firstAgentId", String(data.agent_id))
          } else {
            // Setup succeeded without spawning a default agent (e.g.
            // user picked the "blank" crew template). Clear any stale
            // value from a previous run-through — otherwise the welcome
            // checklist's "Open chat" CTA would deep-link to an agent
            // that no longer exists.
            window.localStorage.removeItem("crewship.firstAgentId")
          }
          // The checklist's "Open chat" needs the slug, not the id. Written
          // and cleared in lockstep with the id above: a stale slug from a
          // previous run-through would send the user to another workspace's
          // agent, which is worse than no deep link at all.
          if (firstAgentSlug) {
            window.localStorage.setItem("crewship.firstAgentSlug", firstAgentSlug)
          } else {
            window.localStorage.removeItem("crewship.firstAgentSlug")
          }
        }
      } catch {
        // localStorage unavailable (private mode) — skip the breadcrumb,
        // dashboard will just not show the banner. Not worth blocking
        // onboarding completion on.
      }
      // The wizard's last click. It used to land on /crews/agents/<id>/chat,
      // a route the /crews redesign deleted — so the very first thing a new
      // user did after setting up was hit a 404. Chat is /chat/<slug> now.
      // If the slug could not be resolved we send them to the dashboard,
      // which carries the welcome checklist and a working "Browse agents":
      // a generic page that works beats a specific one that doesn't.
      setLaunchSummary({ agentSlug: firstAgentSlug || null })
    } catch (e) {
      // Real network failure (no response). Differentiate from the
      // "server returned 5xx" case above so users can tell whether
      // to retry the action or check their connection.
      setError(
        e instanceof Error && e.message
          ? `Couldn't reach the server: ${e.message}. Check your connection and try again.`
          : "Couldn't reach the server. Check your connection and try again.",
      )
      setSubmitting(false)
    }
  }

  async function handleSkip() {
    setSubmitting(true)
    setError(null)
    try {
      const res = await apiFetch("/api/v1/onboarding/complete", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ skipped: true }),
      })
      if (!res.ok && res.status !== 409) {
        const data = await res.json().catch(() => ({}))
        setError(data.error ?? `Could not skip setup (HTTP ${res.status}). Try again.`)
        return
      }
      router.push("/")
    } catch {
      setError("Couldn't reach the server. Setup was not skipped; check your connection and retry.")
    } finally {
      setSubmitting(false)
    }
  }

  if (checking) {
    // The card's own skeleton, not a spinner on a full page; Retry after 10 s
    // re-runs the same fail-closed bootstrap.
    return <SetupCardSkeleton onRetry={() => void bootstrapOnboarding()} />
  }

  if (bootstrapError) {
    return (
      <SetupFrame>
        <div className="w-full max-w-[640px]">
          <PageError
            title="We couldn't restore your setup"
            description={bootstrapError}
            onRetry={() => void bootstrapOnboarding()}
          />
        </div>
      </SetupFrame>
    )
  }

  const adapterCfg = CLI_ADAPTERS[adapter]
  const busy = submitting || persistingCredential || persistingWorkspace
  const reason = blockingReason()
  const nameError =
    workspaceName.length > 0 && workspaceName.trim().length < 2 ? "At least 2 characters" : null
  const hasModelToken = savedCredentialSelected || tokenDelivered || apiKey.trim().length >= 8 || isLocalModel(model)
  const crewStep = step === 3 && !launchSummary
  const chatStep = crewStep && crewMode === "chat"

  // The header names the step; it is the page's h1. Step 1 carries the
  // setup's own title — it is the first thing a new instance shows.
  const header: { title: string; description: string; icon?: typeof Rocket } = launchSummary
    ? { title: "Set up Crewship", description: "All three steps are done.", icon: Check }
    : step === 1
      ? { title: "Set up Crewship", description: "Three steps. Everything can be changed later." }
      : step === 2
        ? {
            // It leads with what the crew needs, not with how the human works:
            // that framing is how the token came to look optional.
            title: "Give your agents a model",
            description: isLocalModel(model)
              ? "Your local model runs on your own endpoint, so no token is needed — just confirm the setup below."
              : "Agents run in containers and need their own token to call a model. Pairing your CLI signs your terminal in; it does not give them one.",
            icon: KeyRound,
          }
        : crewMode === "template"
          ? {
              title: "Pick your first crew",
              description: "Start from a tested team. Every agent can be changed later.",
              icon: Sparkles,
            }
          : {
              title:
                createdCrews.length > 0
                  ? createdCrews.length === 1
                    ? createdCrews[0].proposal.crewName
                    : `${createdCrews.length} crews ready`
                  : preparedProposal
                    ? preparedProposal.crewName
                    : "Tell Crewship Guide what you need",
              description:
                createdCrews.length > 0
                  ? preparedProposal
                    ? "Created so far — and one more waiting for you to press Create in the chat."
                    : "Created. Ask for another crew in the chat, or launch what you have."
                  : preparedProposal
                    ? "Review the crew. Create it from the proposal card in the chat when it looks right."
                    : "Chat with the Guide — it asks a couple of questions, then proposes a crew. Nothing is created until you click Create.",
              icon: Sparkles,
            }

  return (
    <SetupFrame>
      <SetupCard
        size={crewStep ? "xl" : "lg"}
        fill={chatStep}
        aria-labelledby="setup-title"
        onSubmit={() => {
          if (launchSummary || busy || !canContinue()) return
          if (step < 3) void handleContinue()
          else void handleLaunch()
        }}
      >
        <SetupCardHeader title={<span id="setup-title">{header.title}</span>} description={header.description} icon={header.icon} />

        {/* Finished steps are green and clickable (the kit's rule), so a
            resumed setup shows what is already saved and can go back to it.
            Locked while a save or Launch is in flight, like Back. On the
            receipt every step is done and nothing is clickable. */}
        <WizardSteps
          steps={STEP_LABELS}
          current={launchSummary ? STEP_LABELS.length : step - 1}
          onStepChange={launchSummary || busy ? undefined : (i) => setStep((i + 1) as Step)}
          className={launchSummary ? "max-md:hidden [&_.wizard-steps-count]:hidden" : undefined}
        />

        {!launchSummary && step === 1 && (
          <SetupCardBody className="space-y-5">
            <CreateSurfaceField label="Workspace name" htmlFor="workspace_name" error={nameError}>
              <Input
                id="workspace_name"
                value={workspaceName}
                onChange={(e) => setWorkspaceName(e.target.value)}
                placeholder="e.g. Acme Engineering"
                autoFocus
                aria-invalid={nameError ? true : undefined}
                aria-describedby={nameError ? "workspace_name-error" : undefined}
                className="h-8 text-control"
              />
            </CreateSurfaceField>
            <CreateSurfaceField
              label="Agent language"
              htmlFor="language"
              hint="Sets only how your AI agents reply — the Crewship interface stays in English. Change it anytime in Settings → Workspace."
            >
              <LanguagePicker id="language" value={language} onChange={setLanguage} />
            </CreateSurfaceField>

            {/* What the person needs BEFORE step 2, said once and calmly —
                a note, not a warning box, so the first screen of the product
                does not look like something already went wrong. */}
            <Note>
              <div className="font-medium text-foreground">Before you start</div>
              <ul className="mt-1.5 space-y-2">
                <li className="flex items-start gap-2">
                  <KeyRound className="mt-0.5 size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span>
                    Run <code className="font-mono text-foreground">claude setup-token</code> and keep what it prints
                    for step 2. It is a <strong className="font-semibold text-foreground">Claude Code CLI token</strong>,{" "}
                    <em>not</em> an API key from the Anthropic console.
                  </span>
                </li>
                <li className="flex items-start gap-2">
                  <Container className="mt-0.5 size-3.5 shrink-0 text-muted-foreground-soft" aria-hidden />
                  <span className="flex min-w-0 flex-1 flex-wrap items-center gap-x-2 gap-y-1">
                    <span>Keep Docker running on this server — your agents live in containers.</span>
                    <StatusPill
                      data-testid="onboarding-docker-pill"
                      tone={runtimeReady === true ? "success" : runtimeReady === false ? "warn" : "muted"}
                      label={runtimeReady === true ? "Docker detected" : runtimeReady === false ? "Docker not reachable" : "Checking…"}
                    />
                  </span>
                </li>
              </ul>
            </Note>
          </SetupCardBody>
        )}

        {!launchSummary && step === 2 && (
          <SetupCardBody className="space-y-5">
            {/* The container-runtime precondition, at the top of the step it
                blocks. Fail at the choice with an explanation, rather than two
                steps later as an error naming an internal component. */}
            {runtimeInUse !== true && (
              <SetupBanner
                role="alert"
                data-testid="onboarding-runtime-blocker"
                action={
                  runtimeInUse === null || runtimeChecking ? undefined : (
                    <Button type="button" variant="outline" size="sm" onClick={() => void checkRuntime()} disabled={runtimeChecking}>
                      Check again
                    </Button>
                  )
                }
              >
                {runtimeInUse === null || runtimeChecking
                  ? "Checking for a container runtime…"
                  : // Two different failures with two different fixes, so
                    // they must not share a sentence.
                    runtimeReady
                    ? "Docker is running, but this Crewship server isn't using it — it was started without a container runtime. Restart the server so it picks Docker up."
                    : "Docker isn't reachable, so agents can't start. Install or start Docker, then check again."}
              </SetupBanner>
            )}

            {/* MODE PICKER. Browser first: the person this screen is for is
                installing Crewship for the first time and may not have the CLI
                at all. Neither tile changes what the agents need — the model
                and its token are asked for below in both cases. */}
            <CreateSurfaceField label="Where you work">
              <div className="grid gap-2 sm:grid-cols-2">
                <ChoiceTile
                  icon={MessageSquare}
                  title="Chat in browser"
                  description="Nothing to install."
                  active={mode === "browser"}
                  onClick={() => setMode("browser")}
                />
                <ChoiceTile
                  icon={Terminal}
                  title="Also pair my CLI"
                  description="Optional — signs your terminal in too."
                  active={mode === "cli"}
                  onClick={() => setMode("cli")}
                />
              </div>
            </CreateSurfaceField>

            {mode === "cli" && (
              <div className="space-y-2" data-testid="onboarding-pairing">
                <p className="text-label leading-relaxed text-muted-foreground">
                  Don&apos;t have the Crewship CLI yet? Download it from{" "}
                  <a
                    href="https://github.com/crewship-ai/crewship/releases"
                    target="_blank"
                    rel="noopener noreferrer"
                    className="font-medium text-primary underline-offset-2 hover:underline"
                  >
                    GitHub releases
                  </a>
                  , then run this on the machine where it lives:
                </p>
                <PairingBlock
                  status={pairStatus}
                  command={pairCommand}
                  remainingSec={pairRemainingSec}
                  copied={pairCopied}
                  onCopy={copyPairCmd}
                  paired={
                    tokenDelivered ? (
                      <>
                        <strong className="font-semibold text-foreground">CLI paired and model token received.</strong>{" "}
                        Nothing to paste.
                      </>
                    ) : (
                      <>
                        <strong className="font-semibold text-foreground">CLI paired.</strong> Your terminal is signed in;
                        the agents still need their own model token.
                      </>
                    )
                  }
                  onNewCode={() => {
                    // startPairing flips to "starting" synchronously before its
                    // await, so this does not bridge through "idle" — that would
                    // briefly open the auto-start race window.
                    setPairCode(null)
                    setPairExpiresAt(null)
                    void startPairing()
                  }}
                  onRetry={() => void startPairing()}
                />
              </div>
            )}

            {/* ADAPTER + MODEL + TOKEN. Always rendered, in both modes: the
                model is a fact about the AGENTS, and hiding it behind the CLI
                choice is how the model picker once disappeared. */}
            <CreateSurfaceField label="Agent toolchain">
              <ToolchainPicker
                value={adapter}
                onChange={(key) => {
                  setAdapter(key)
                  setModel(CLI_ADAPTERS[key].defaultModel)
                }}
              />
              {CLI_ADAPTERS[adapter]?.status !== "production" && (
                <Note tone="warn" role="alert">
                  {CLI_ADAPTERS[adapter]?.label} is still experimental and its CLI is not guaranteed to be present in the
                  onboarding image. Choose Claude Code to finish setup; you can add experimental adapters from the
                  dashboard afterwards.
                </Note>
              )}
            </CreateSurfaceField>

            <CreateSurfaceField label="Model" htmlFor="model">
              <Select value={model} onValueChange={setModel}>
                <SelectTrigger id="model" className="h-8 font-mono text-label">
                  <SelectValue placeholder="Select model" />
                </SelectTrigger>
                <SelectContent>
                  {getModelsForAdapter(adapter).map((m) => (
                    <SelectItem key={m.value} value={m.value} className="font-mono text-label">
                      {m.label}
                      {m.value === adapterCfg?.defaultModel ? " · recommended" : ""}
                      {m.category === "legacy" ? " · older" : ""}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </CreateSurfaceField>

            <div className="space-y-2">
              <div className="flex flex-col items-start gap-1 sm:flex-row sm:items-center sm:justify-between sm:gap-2">
                <label htmlFor="api_key" className="text-micro font-medium uppercase tracking-wider text-muted-foreground">
                  {adapterCfg?.label ?? "Adapter"} CLI token
                </label>
                {ADAPTER_TOKEN_GUIDE[adapter] && (
                  <a
                    href={ADAPTER_TOKEN_GUIDE[adapter].url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="inline-flex items-center gap-0.5 text-label text-primary hover:underline"
                  >
                    {ADAPTER_TOKEN_GUIDE[adapter].label}
                    <ExternalLink className="size-3" aria-hidden />
                  </a>
                )}
              </div>
              {/* A token saved on the server is never sent back to this page.
                  It reads "Token saved" with Replace; an empty input after
                  Replace still means "reuse it", not "delete it". */}
              {(() => {
                const locked = savedCredentialSelected && !replacingSaved
                return (
                  <div className="flex items-center gap-2">
                    <div className="relative min-w-0 flex-1">
                      <Input
                        id="api_key"
                        type={showApiKey ? "text" : "password"}
                        value={apiKey}
                        onChange={(e) => setApiKey(e.target.value)}
                        disabled={locked}
                        placeholder={
                          locked
                            ? "Token saved — encrypted on the server"
                            : savedCredentialSelected
                              ? "Saved token — leave blank to reuse"
                              : "Paste your CLI token (starts with sk-ant-oat…)"
                        }
                        autoComplete="off"
                        spellCheck={false}
                        className="h-8 pr-9 font-mono text-label"
                      />
                      {!locked && (
                        <button
                          type="button"
                          onClick={() => setShowApiKey((v) => !v)}
                          aria-label={showApiKey ? "Hide token" : "Show token"}
                          aria-pressed={showApiKey}
                          className="absolute right-1 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                        >
                          {showApiKey ? <EyeOff className="size-3.5" /> : <Eye className="size-3.5" />}
                        </button>
                      )}
                    </div>
                    {locked && (
                      <Button type="button" variant="outline" size="sm" onClick={() => setReplacingSaved(true)}>
                        Replace
                      </Button>
                    )}
                  </div>
                )
              })()}
              {(savedCredentialSelected || tokenDelivered) && (
                <Note icon={Check}>
                  <span className="font-medium text-foreground">
                    {savedCredentialSelected
                      ? "Your saved Anthropic credential will be reused."
                      : "Your CLI already handed over a token."}
                  </span>{" "}
                  {savedCredentialSelected
                    ? "The secret stays encrypted and is not sent back to this page."
                    : "Change it only if you want the agents on a different credential."}
                </Note>
              )}
              {/* Says what the pasted value IS before Continue asks the
                  provider — the one mistake this field exists to catch. */}
              {tokenHint && (
                <Note
                  tone={tokenHint.tone === "warn" ? "warn" : "info"}
                  icon={tokenHint.tone === "ok" ? Check : undefined}
                  role={tokenHint.tone === "warn" ? "alert" : "status"}
                  data-testid="onboarding-token-hint"
                >
                  {tokenHint.text}
                </Note>
              )}
              {isLocalModel(model) ? (
                <Note>
                  Local model selected — no API key needed. Leave this empty unless you also want cloud models later.
                  Requires <code className="font-mono text-foreground">CREWSHIP_LOCAL_MODEL_BASE_URL</code> on the server.
                </Note>
              ) : (
                <>
                  {ADAPTER_TOKEN_CMD[adapter] && (
                    <CommandSnippet
                      command={ADAPTER_TOKEN_CMD[adapter]}
                      caption="Run this on your machine, then paste the output above:"
                    />
                  )}
                  <p className="text-label leading-relaxed text-muted-foreground-soft">
                    The <strong className="font-medium text-muted-foreground">CLI token</strong> from{" "}
                    <code className="font-mono">{ADAPTER_TOKEN_CMD[adapter] ?? "<cli> setup-token"}</code>, <em>not</em> the
                    raw account API key from the provider&apos;s console — an sk-ant-api… key here won&apos;t work.
                  </p>
                </>
              )}
            </div>

            {mode === "cli" && ADAPTER_CLI_INSTALL[adapter] && (
              <Note icon={Sparkles}>
                New to {adapterCfg?.label}?{" "}
                <a
                  href={ADAPTER_CLI_INSTALL[adapter].url}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-0.5 text-primary hover:underline"
                >
                  {ADAPTER_CLI_INSTALL[adapter].label}
                  <ExternalLink className="size-3" aria-hidden />
                </a>
                {" — "}then come back and paste the snippet above.
              </Note>
            )}

            {/* TELEMETRY CONSENT — explicit choice, pre-ticked to the build's
                default (seeded from /api/v1/system/telemetry). Sticky
                server-side, same as `crewship telemetry on|off`. */}
            <CreateSurfaceToggleRow
              label="Send anonymous crash reports"
              hint={
                <>
                  Helps the maintainer fix bugs. Stack traces and version info only — never your workspace data,
                  credentials, or prompts. Change anytime with{" "}
                  <code className="font-mono">crewship telemetry on|off</code>.
                </>
              }
              control={
                <Checkbox
                  id="telemetry_opt_in"
                  aria-label="Send anonymous crash reports"
                  checked={telemetryOptIn}
                  onCheckedChange={(v) => setTelemetryOptIn(v === true)}
                />
              }
            />
          </SetupCardBody>
        )}

        {chatStep && (
          <SetupCardBody
            scroll={false}
            className="flex flex-col max-md:overflow-y-auto md:grid md:grid-cols-[minmax(0,1fr)_320px]"
          >
            {/* Left: the conversation (PRD §4.1). On a phone it comes after
                the collapsible "Built so far". */}
            <div className="order-2 flex min-h-[460px] flex-col border-hairline max-md:border-t md:order-1 md:min-h-0 md:border-r">
              <OnboardingSetupChat
                embedded
                onUnavailable={handleSetupAgentUnavailable}
                onProposalApplied={handleProposalApplied}
                onProposalPrepared={setPreparedProposal}
                language={language}
              />
            </div>

            {/* Right: what exists. Read back from the workspace, not from the
                transcript — routines and pages are made by the agent inside
                its container and the wizard never hears about them. */}
            <div className="order-1 min-h-0 space-y-3 px-4 py-3 md:order-2 md:overflow-y-auto md:py-4">
              <button
                type="button"
                aria-expanded={builtOpen}
                onClick={() => setBuiltOpen((v) => !v)}
                className="flex w-full items-center justify-between text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft md:hidden"
              >
                Built so far
                <ChevronDown className={cn("size-3.5 transition-transform", builtOpen && "rotate-180")} aria-hidden />
              </button>
              <div className={cn("space-y-3", !builtOpen && "max-md:hidden")}>
                <div className="text-micro font-semibold uppercase tracking-wider text-muted-foreground-soft max-md:hidden">
                  Built so far
                </div>
                {/* The three beats of this step, while there is nothing to
                    show yet. Gone the moment there is a proposal or a crew. */}
                {createdCrews.length === 0 && !preparedProposal && existingCrewCount === 0 && (
                  <ol className="space-y-2 rounded-lg bg-muted p-3 text-label" aria-label="How this step works">
                    {[
                      ["Describe the work", "Pick a starter prompt or write it in your own words."],
                      ["Review the proposal", "The Guide answers with a crew: agents, roles, model and network access."],
                      ["Create, then Launch", "Create builds the crew. Launch finishes setup and opens the chat."],
                    ].map(([title, body], i) => (
                      <li key={title} className="flex items-start gap-2.5">
                        <span className="mt-0.5 flex size-5 shrink-0 items-center justify-center rounded-full bg-card font-mono text-micro font-semibold text-muted-foreground">
                          {i + 1}
                        </span>
                        <span className="min-w-0">
                          <span className="block font-medium text-foreground">{title}</span>
                          <span className="block leading-relaxed text-muted-foreground">{body}</span>
                        </span>
                      </li>
                    ))}
                  </ol>
                )}
                {/* Every crew that really exists, then the one still awaiting
                    Create — `created` is per proposal, so a fresh second
                    proposal never wears the first one's "Created". */}
                {createdCrews.map((c) => (
                  <OnboardingProposalSummary key={c.id} proposal={c.proposal} created />
                ))}
                {preparedProposal && <OnboardingProposalSummary proposal={preparedProposal} created={false} />}
                {createdCrews.length === 0 && existingCrewCount > 0 && (
                  <Note icon={Check} role="status" data-testid="onboarding-resumed-crews">
                    <span className="font-medium text-foreground">
                      {existingCrewCount === 1 ? "A crew was already built" : `${existingCrewCount} crews were already built`}
                    </span>{" "}
                    before this page was reloaded — it is listed below. You can launch with it now, or ask the Guide for
                    another. Do not create the same crew twice.
                  </Note>
                )}
                <OnboardingCreatedPanel workspaceId={onboardingWorkspaceId} onCrewsFound={setExistingCrewCount} />
                {/* Escape hatch (PRD §4.3). Hidden once a crew exists —
                    switching away then would abandon a real crew. */}
                {createdCrews.length === 0 && (
                  <button
                    type="button"
                    onClick={() => setCrewMode("template")}
                    className="text-label font-medium text-primary underline-offset-2 hover:underline"
                  >
                    Pick a template instead
                  </button>
                )}
              </div>
            </div>
          </SetupCardBody>
        )}

        {crewStep && crewMode === "template" && (
          <SetupCardBody className="space-y-4">
            {/* WHY the chat is not showing, never a silent swap. A real outage
                (chatUnavailable) offers no way back to something that will
                just fail again; a missing token is recoverable and keeps it. */}
            {chatUnavailable && (
              <SetupBanner role="status" data-testid="onboarding-guide-unavailable">
                Crewship Guide can&apos;t be reached right now. Nothing is lost: pick a template and launch — you can
                talk to the Guide from the dashboard afterwards.
              </SetupBanner>
            )}
            {chatNeedsCredential && !chatUnavailable && (
              <Note
                tone="warn"
                data-testid="onboarding-guide-needs-token"
              >
                <span className="flex flex-wrap items-center justify-between gap-2">
                  <span>The Guide needs your model token from step 2. Pick a template for now, or go back and add it.</span>
                  <Button type="button" variant="outline" size="sm" onClick={() => setStep(2)} disabled={busy}>
                    Back to step 2
                  </Button>
                </span>
              </Note>
            )}
            <TemplatePicker
              value={crewSlug}
              onChange={setCrewSlug}
              hasModelToken={hasModelToken}
              runtimeInUse={runtimeInUse}
            />
            {!chatUnavailable && (
              <button
                type="button"
                onClick={() => {
                  // Clear the credential note so a retry starts clean — the
                  // chat remounts fresh and re-checks the precondition itself.
                  setChatNeedsCredential(false)
                  setCrewMode("chat")
                }}
                className="text-label font-medium text-primary underline-offset-2 hover:underline"
              >
                Talk to Crewship Guide instead
              </button>
            )}
          </SetupCardBody>
        )}

        {launchSummary && (
          <SetupCardBody className="space-y-4">
            <div className="flex flex-col items-center gap-2 py-3 text-center">
              <span className="flex size-7 items-center justify-center rounded-full bg-success text-white" aria-hidden>
                <Check className="size-4" />
              </span>
              <h2 className="text-heading font-semibold tracking-tight text-foreground">
                {/* Zero is a real case: the template path creates its crew
                    inside POST /onboarding/setup and reports no roster back,
                    so "Your 0 crews are ready" over an empty list is wrong. */}
                {createdCrews.length <= 1 ? "Your crew is ready" : `Your ${createdCrews.length} crews are ready`}
              </h2>
              <p className="text-body text-muted-foreground">
                {createdCrews.length === 0
                  ? "Setup is complete — your crew is deployed and ready to talk to."
                  : "Here is what Crewship just built for you."}
              </p>
            </div>
            {createdCrews.length > 0 && (
              <ul className="divide-y divide-hairline overflow-hidden rounded-lg border border-hairline">
                {createdCrews.map((c) => (
                  <li key={c.id}>
                    <details className="group">
                      <summary className="flex cursor-pointer list-none items-center gap-3 px-3 py-2.5 coarse:min-h-[44px]">
                        {c.proposal.crewIcon && (
                          <CrewIcon icon={c.proposal.crewIcon} color={c.proposal.crewColor} size="sm" />
                        )}
                        <span className="min-w-0 flex-1 truncate text-body font-medium">{c.proposal.crewName}</span>
                        <span className="shrink-0 text-label text-muted-foreground">
                          {c.proposal.agents.length} {c.proposal.agents.length === 1 ? "agent" : "agents"}
                        </span>
                        <ChevronDown className="size-3.5 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
                      </summary>
                      <ul className="space-y-1 px-3 pb-3 pl-13">
                        {c.proposal.agents.map((a) => (
                          <li key={a.name} className="flex items-baseline justify-between gap-3 text-label">
                            <span className="text-muted-foreground">
                              <span className="text-foreground">{a.name}</span>
                              {a.role ? ` — ${a.role}` : ""}
                            </span>
                            <span className="shrink-0 font-mono text-micro text-muted-foreground">{getModelLabel(a.model)}</span>
                          </li>
                        ))}
                      </ul>
                    </details>
                  </li>
                ))}
              </ul>
            )}
          </SetupCardBody>
        )}

        {error && (
          <div
            role="alert"
            aria-live="assertive"
            className="shrink-0 border-t border-hairline bg-chip-danger-bg px-4 py-2 text-label text-chip-danger-fg md:px-5"
          >
            {error}
          </div>
        )}

        {launchSummary ? (
          <SetupCardFooter>
            {launchSummary.agentSlug && (
              <Button variant="outline" size="sm" onClick={() => router.push("/")}>
                Go to dashboard
              </Button>
            )}
            <Button
              size="sm"
              onClick={() =>
                router.push(launchSummary.agentSlug ? `/chat/${encodeURIComponent(launchSummary.agentSlug)}` : "/")
              }
            >
              {launchSummary.agentSlug ? "Start chatting" : "Go to dashboard"}
              <ArrowRight className="size-3.5" aria-hidden />
            </Button>
          </SetupCardFooter>
        ) : (
          <SetupCardFooter
            hint={
              reason && !busy ? (
                <span data-testid="onboarding-blocking-reason" aria-live="polite">
                  {reason}
                </span>
              ) : (
                <ShortcutHint label={step < 3 ? "continue" : "launch"} />
              )
            }
          >
            {step > 1 && (
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setStep((s) => (s > 1 ? ((s - 1) as Step) : s))}
                // Locked while Launch or a step's save is in flight, so Back
                // cannot race POST /credentials or /setup.
                disabled={busy}
              >
                <ArrowLeft className="size-3.5" aria-hidden />
                Back
              </Button>
            )}
            <Button type="button" variant="outline" size="sm" onClick={() => setSkipDialogOpen(true)} disabled={busy}>
              Skip setup
            </Button>
            {step < 3 ? (
              <Button
                size="sm"
                onClick={() => void handleContinue()}
                disabled={!canContinue() || busy}
                aria-busy={persistingCredential || persistingWorkspace || undefined}
              >
                {persistingCredential ? "Verifying token…" : persistingWorkspace ? "Saving…" : "Continue"}
                <ArrowRight className="size-3.5" aria-hidden />
              </Button>
            ) : (
              <Button size="sm" onClick={handleLaunch} disabled={!canContinue() || submitting} aria-busy={submitting || undefined}>
                <Rocket className="size-3.5" aria-hidden />
                {submitting ? "Launching…" : "Launch"}
              </Button>
            )}
          </SetupCardFooter>
        )}
      </SetupCard>

      {/* Skip is one click from Continue and cannot be undone in the sense
          that matters — this setup never opens again — so it asks first. */}
      <ConfirmDialog
        open={skipDialogOpen}
        onOpenChange={setSkipDialogOpen}
        title="Skip setup?"
        description="You land on an empty dashboard with no crew and no model token, and this setup does not open again. A checklist there walks you through it."
        cancelLabel="Keep going"
        confirmLabel="Skip anyway"
        onConfirm={handleSkip}
      />
    </SetupFrame>
  )
}

/**
 * Searchable language picker — Popover + cmdk Command, same pattern
 * Settings → General uses so a user who lands first in onboarding
 * and later opens settings sees the identical control. Searches
 * English name, native name, AND ISO code so a user who only
 * remembers "cs" or "Čeština" still finds Czech.
 *
 * Stores the English `name` (e.g. "Czech") in the parent state so it
 * lands verbatim in workspaces.preferred_language. The orchestrator
 * injects that string into every agent's system prompt.
 */
function LanguagePicker({
  id,
  value,
  onChange,
}: {
  id?: string
  value: string
  onChange: (v: string) => void
}) {
  const [open, setOpen] = useState(false)
  const selected = LANGUAGES.find((l) => l.name === value)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          id={id}
          type="button"
          aria-label="Pick a language"
          className="flex h-8 w-full items-center justify-between rounded-md border border-control-border bg-card px-2.5 text-control transition-colors hover:border-line-strong"
        >
          {selected ? (
            <span className="inline-flex items-center gap-2 truncate">
              <span className="leading-none">{selected.flag}</span>
              <span className="truncate">{selected.name}</span>
              <span className="truncate text-label text-muted-foreground">· {selected.native}</span>
            </span>
          ) : (
            <span className="text-muted-foreground">Select language…</span>
          )}
          <ChevronsUpDown className="size-3.5 shrink-0 text-muted-foreground" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
        <Command
          filter={(itemValue, search) => {
            // itemValue is the English name set on each CommandItem. Match on
            // English name, native name and ISO code.
            const lang = LANGUAGES.find((l) => l.name === itemValue)
            if (!lang) return 0
            const s = search.toLowerCase()
            if (!s) return 1
            return lang.name.toLowerCase().includes(s) ||
              lang.native.toLowerCase().includes(s) ||
              lang.code.toLowerCase().includes(s)
              ? 1
              : 0
          }}
        >
          <CommandInput placeholder="Search language…" />
          <CommandList>
            <CommandEmpty>No language found.</CommandEmpty>
            <CommandGroup>
              {LANGUAGES.map((lang) => (
                <CommandItem
                  key={lang.code}
                  value={lang.name}
                  onSelect={() => {
                    onChange(lang.name)
                    setOpen(false)
                  }}
                  className="text-control"
                >
                  <span className="mr-2 leading-none">{lang.flag}</span>
                  <span>{lang.name}</span>
                  <span className="ml-auto text-micro text-muted-foreground">{lang.native}</span>
                  {value === lang.name && <Check className="ml-2 size-3.5 text-primary" />}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
