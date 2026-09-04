"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import {
  ArrowRight,
  ArrowLeft,
  Rocket,
  Globe,
  Terminal,
  Copy,
  Check,
  ExternalLink,
  Sparkles,
  AlertTriangle,
  ChevronsUpDown,
  Building2,
  Cpu,
  Users,
  Eye,
  EyeOff,
  Container,
  KeyRound,
  Clock,
} from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { CrewshipLogo } from "@/components/branding/crewship-logo"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Checkbox } from "@/components/ui/checkbox"
import { CrewIcon } from "@/components/ui/crew-icon"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelsForAdapter, isLocalModel } from "@/lib/cli-adapters"
import { ToolchainPicker } from "@/components/features/onboarding/toolchain-picker"
import { CommandSnippet, copyText } from "@/components/features/onboarding/command-snippet"
import { buildOnboardingSetupBody } from "@/lib/onboarding-setup"
import { ADAPTER_TOKEN_GUIDE, ADAPTER_TOKEN_CMD, ADAPTER_CLI_INSTALL } from "@/lib/cli-adapter-brand"
import { LANGUAGES } from "@/lib/languages"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import { serverFetch } from "@/lib/server-base"
import { apiFetch } from "@/lib/api-fetch"
import {
  OnboardingPreview,
  TEMPLATES,
  type CrewTemplateSlug,
  type HandoffMode,
} from "@/components/features/onboarding/onboarding-preview"
import { OnboardingSetupChat } from "@/components/features/onboarding/onboarding-setup-chat"
import { OnboardingCreatedPanel } from "@/components/features/onboarding/onboarding-created-panel"
import { OnboardingProposalSummary } from "@/components/features/onboarding/onboarding-proposal-summary"
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
 * Variant D — split-screen onboarding. Left pane: form with vertical
 * stepper (Workspace → Adapter → Crew). Right pane: live preview that
 * animates as the user makes choices. On <lg breakpoints the preview
 * collapses below the form into a single column.
 *
 * Step order matters here in a way it wouldn't for an ordinary form: the
 * Crew step's default is a chat with a setup agent that runs in a
 * container and needs a model credential to answer at all (see
 * onboarding-setup-chat.tsx and internal/api/onboarding_setup_agent.go).
 * Adapter must come before Crew so that credential exists by the time the
 * chat opens — see `persistAdapterCredential` below for how the token
 * actually lands in the database before step 3 renders.
 *
 * Visual language tracks crewship-web — Apple-tight easing on all
 * motion (cubic-bezier 0.16, 1, 0.3, 1, ~400ms), Geist sans, brand
 * blue gradient logo tile, brand-coloured provider icons (Anthropic
 * peach, OpenAI green, Google blue, Cursor cyan, Factory amber).
 *
 * The form fields are wrapped in AnimatePresence with mode="wait" so
 * step transitions feel like the marketing-site section reveals
 * rather than a hard swap.
 */

/** Apple-tight easing — same curve crewship-web uses on hero reveal. */
const ease = [0.16, 1, 0.3, 1] as const

/**
 * Crew picker list — sourced from the same TEMPLATES map the preview
 * uses so the row icon + tint and the right-pane card match. Adding
 * a 5th builtin template only needs an entry in onboarding-preview.tsx
 * and the seed file; this list reads from the map.
 */
const CREW_OPTIONS: { slug: CrewTemplateSlug; label: string }[] = [
  { slug: "software-development", label: "Software Development" },
  { slug: "devops-sre", label: "DevOps / SRE" },
  { slug: "content-marketing", label: "Content Marketing" },
  { slug: "accounting-finance", label: "Accounting & Finance" },
  { slug: "blank", label: "Start blank" },
]

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
  const reduce = useReducedMotion()
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
  const [pairStatus, setPairStatus] = useState<"idle" | "starting" | "pending" | "consumed" | "expired" | "failed">("idle")
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
        // eslint-disable-next-line no-restricted-syntax -- onboarding-time credential probe; mirrors the pairing poll above
        const res = await fetch("/api/v1/credentials")
        if (!res.ok) return
        const data = await res.json()
        const list = Array.isArray(data) ? data : (data?.credentials ?? [])
        const found = list.some(
          (c: { provider?: string; status?: string }) =>
            (c.provider ?? "").toUpperCase() === "ANTHROPIC" &&
            (!c.status || c.status.toUpperCase() === "ACTIVE"),
        )
        if (found && !cancelled) setTokenDelivered(true)
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
  }, [mode, step, pairStatus, tokenDelivered])

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
      if (runtimeInUse !== true) return "Waiting for a container runtime — see the note above."
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
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Spinner className="h-8 w-8 text-muted-foreground" />
      </div>
    )
  }

  if (bootstrapError) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background p-6">
        <div className="w-full max-w-md rounded-2xl border border-border bg-card p-6 text-center shadow-sm">
          <AlertTriangle className="mx-auto h-7 w-7 text-warn" />
          <h1 className="mt-3 text-lg font-semibold">We couldn&apos;t restore your setup</h1>
          <p className="mt-2 text-sm text-muted-foreground">{bootstrapError}</p>
          <Button className="mt-5" onClick={() => void bootstrapOnboarding()}>
            Retry
          </Button>
        </div>
      </div>
    )
  }

  const adapterCfg = CLI_ADAPTERS[adapter]

  return (
    <div className="min-h-screen bg-background lg:h-screen lg:overflow-hidden">
      {/* Subtle hero glow — same radial gradient idea from
          crewship-web's .hero-glow but anchored to the top of the
          form column so the form has a sense of stage lighting
          without distracting from the live preview. */}
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[360px] bg-[radial-gradient(ellipse_60%_50%_at_30%_0%,rgba(30,123,254,0.10),transparent_60%)]" />

      <div className="relative grid min-h-screen grid-cols-1 lg:h-screen lg:min-h-0 lg:grid-cols-2">
        {/* LEFT: form.
            Anchored to the top, not centred. Centring the whole column meant
            the lockup and the stepper slid up and down as the step content
            changed height — measured at y=101 on Workspace, y=137 on Crew and
            y=66 on Adapter, so the logo visibly jumped on every Continue. The
            fixed things stay fixed; only the form below them moves. */}
        {/* pb-0, and the nav row below carries the bottom inset instead. The
            nav is `sticky bottom-0`, so any padding left on THIS box would be
            a transparent strip under the pinned bar with crew cards sliding
            through it. */}
        <div className="flex items-start border-b border-border p-6 pb-0 lg:h-screen lg:overflow-y-auto lg:border-b-0 lg:border-r lg:p-12 lg:pb-0">
          <div className="touch-form w-full max-w-md mx-auto space-y-7 lg:pt-6">
            <motion.div
              initial={reduce ? { opacity: 0 } : { opacity: 0, y: -8 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, ease }}
              className="flex items-center gap-3"
            >
              {/* The bare cropped mark, matching the sign-in lockup. Inside
                  the tile's padding AND the viewBox's, the sails stopped
                  being legible at this size. */}
              <CrewshipLogo tight className="h-9 w-auto text-foreground" />
              <div>
                <div className="font-mono text-[10px] uppercase tracking-[0.14em] text-muted-foreground">Setup</div>
                <h1 className="text-lg font-bold tracking-tight">Crewship</h1>
              </div>
            </motion.div>

            <VerticalStepper step={step} />

            <AnimatePresence mode="wait">
              <motion.div
                key={step}
                initial={reduce ? { opacity: 0 } : { opacity: 0, y: 12 }}
                animate={{ opacity: 1, y: 0 }}
                exit={reduce ? { opacity: 0 } : { opacity: 0, y: -8 }}
                transition={{ duration: 0.4, ease }}
                className="space-y-5"
              >
                {!launchSummary && step === 1 && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">What&apos;s your workspace called?</h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        A workspace holds your crews, agents, and credentials. You can rename it later.
                      </p>
                    </div>

                    {/* What the person needs BEFORE step 2, said once and
                        calmly. This used to be an amber warning box, which
                        made the very first screen of the product look like
                        something had already gone wrong. It is a checklist:
                        two things to have ready, plus the runtime check the
                        page runs on its own. */}
                    <div className="rounded-2xl border border-border bg-card/60 p-4 text-xs leading-relaxed">
                      <div className="mb-3 flex items-center justify-between gap-2">
                        <div className="font-medium text-foreground">Before you start</div>
                        <div className="inline-flex items-center gap-1 text-[11px] text-muted-foreground">
                          <Clock className="h-3 w-3" /> about 3 minutes
                        </div>
                      </div>
                      <ol className="space-y-3">
                        <li className="flex items-start gap-3">
                          <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-lg border border-border bg-background">
                            <KeyRound className="h-3.5 w-3.5 text-muted-foreground" />
                          </span>
                          <div className="min-w-0 flex-1 space-y-1.5">
                            <div className="text-foreground/90">
                              A <strong className="font-semibold">Claude Code CLI token</strong> for your agents
                              {" "}— <em>not</em> an API key from the Anthropic console. Run this in a terminal
                              and keep the output for step 2:
                            </div>
                            <CommandSnippet command="claude setup-token" />
                          </div>
                        </li>
                        <li className="flex items-start gap-3">
                          <span className="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-lg border border-border bg-background">
                            <Container className="h-3.5 w-3.5 text-muted-foreground" />
                          </span>
                          <div className="min-w-0 flex-1">
                            <div className="text-foreground/90">
                              <strong className="font-semibold">Docker</strong> running on this server — your
                              agents live in containers.
                            </div>
                            {runtimeReady === true && (
                              <motion.div
                                initial={{ opacity: 0, x: -6 }}
                                animate={{ opacity: 1, x: 0 }}
                                transition={{ duration: 0.35, ease, delay: 0.15 }}
                                className="mt-1 inline-flex items-center gap-1.5 rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[11px] font-medium text-success"
                              >
                                <Check className="h-3 w-3" /> Docker detected
                              </motion.div>
                            )}
                            {runtimeReady === false && (
                              <div className="mt-1 inline-flex items-center gap-1.5 rounded-full border border-warn/30 bg-warn/10 px-2 py-0.5 text-[11px] font-medium text-warn">
                                <AlertTriangle className="h-3 w-3" /> Docker isn&apos;t reachable — start it before the last step
                              </div>
                            )}
                            {runtimeReady === null && (
                              <div className="mt-1 inline-flex items-center gap-1.5 text-[11px] text-muted-foreground">
                                <Spinner className="h-3 w-3" /> Checking…
                              </div>
                            )}
                          </div>
                        </li>
                      </ol>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="workspace_name">Workspace name</Label>
                      <Input
                        id="workspace_name"
                        value={workspaceName}
                        onChange={(e) => setWorkspaceName(e.target.value)}
                        placeholder="e.g. Acme Engineering"
                        autoFocus
                        className="h-11"
                      />
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="language">Agent language</Label>
                      <LanguagePicker id="language" value={language} onChange={setLanguage} />
                      <p className="text-[11px] text-muted-foreground leading-relaxed">
                        Sets only how your AI agents reply — the Crewship interface stays in English. Change it
                        anytime in Settings → Workspace.
                      </p>
                    </div>
                  </div>
                )}

                {!launchSummary && step === 3 && crewMode === "chat" && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">
                        {createdCrews.length > 0
                          ? createdCrews.length === 1
                            ? createdCrews[0].proposal.crewName
                            : `${createdCrews.length} crews ready`
                          : preparedProposal
                            ? preparedProposal.crewName
                            : "Tell Crewship Guide what you need"}
                      </h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        {createdCrews.length > 0
                          ? preparedProposal
                            ? "Created so far — and one more waiting for you to press Create in the chat."
                            : "Created. Ask for another crew in the chat, or launch what you have."
                          : preparedProposal
                            ? "Review the crew below. Create it from the proposal card in the chat when it looks right."
                            : "Chat with the Guide — it asks a couple of questions, then proposes a crew. Nothing is created until you click Create."}
                      </p>
                    </div>
                    {/* The three beats of this step, spelled out while the
                        pane on the left would otherwise be a heading and a
                        disabled Launch button. Gone the moment there is a
                        proposal or a crew to show instead. */}
                    {createdCrews.length === 0 && !preparedProposal && (
                      <ol className="space-y-2 rounded-2xl border border-border bg-card/60 p-4 text-xs" aria-label="How this step works">
                        {[
                          ["Describe the work", "Pick a starter prompt or write it in your own words."],
                          ["Review the proposal", "The Guide answers with a crew: agents, roles, model and network access."],
                          ["Create, then Launch", "Create builds the crew. Launch finishes setup and opens the chat."],
                        ].map(([title, body], i) => (
                          <li key={title} className="flex items-start gap-3">
                            <span className="mt-0.5 flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-primary/10 font-mono text-[10px] font-semibold text-primary">
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
                    {/* Every crew that really exists, then the one still
                        awaiting Create. `created` is per-proposal — it used to
                        be `appliedProposal !== null`, i.e. "has ANY crew been
                        created", so a freshly proposed second crew rendered
                        with a green "Created" badge while nothing had been
                        written for it yet. The panel was lying at exactly the
                        moment the user was deciding whether to click. */}
                    {createdCrews.map((c) => (
                      <OnboardingProposalSummary key={c.id} proposal={c.proposal} created />
                    ))}
                    {preparedProposal && (
                      <OnboardingProposalSummary proposal={preparedProposal} created={false} />
                    )}
                    {/* Everything the Guide has ACTUALLY created, read back
                        from the workspace rather than from the transcript.
                        Routines and pages are made by the agent calling its
                        own tools inside a container, so the wizard never
                        hears about them — without this the person is told in
                        prose that a routine exists and shown a panel that
                        says nothing. */}
                    {createdCrews.length === 0 && existingCrewCount > 0 && (
                      <div
                        role="status"
                        data-testid="onboarding-resumed-crews"
                        className="rounded-xl border border-success/30 bg-success/5 p-3 text-xs leading-relaxed text-muted-foreground"
                      >
                        <span className="font-medium text-success">
                          {existingCrewCount === 1 ? "A crew was already built" : `${existingCrewCount} crews were already built`}
                        </span>{" "}
                        before this page was reloaded — it is listed below. You can launch with it now, or ask the
                        Guide for another. Do not create the same crew twice.
                      </div>
                    )}
                    <OnboardingCreatedPanel workspaceId={onboardingWorkspaceId} onCrewsFound={setExistingCrewCount} />
                    {/* Escape hatch (PRD §4.3): a user who already knows what
                        they want must still be able to skip straight to a
                        template. Hidden once a proposal is actually applied —
                        switching away at that point would abandon a crew that
                        already exists, not merely a choice. */}
                    {createdCrews.length === 0 && (
                      <button
                        type="button"
                        onClick={() => setCrewMode("template")}
                        className="text-xs font-medium text-primary underline-offset-2 hover:underline"
                      >
                        Prefer to pick a template instead? →
                      </button>
                    )}
                  </div>
                )}

                {!launchSummary && step === 3 && crewMode === "template" && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">Pick your first crew</h2>
                      <p className="text-sm text-muted-foreground mt-1">Watch the preview build itself on the right.</p>
                    </div>
                    {/* Explains WHY the chat pane isn't showing rather than
                        landing here with no context — the one outcome this
                        whole feature must avoid is a chat box that silently
                        never answers. See handleSetupAgentUnavailable's own
                        comment: this is expected/recoverable, not a failure,
                        so it stays visually distinct from chatUnavailable's
                        "the setup agent is broken" framing below.

                        Unlike the old step order, there is no LATER step
                        that still collects a token — step 2 already asked.
                        So the recovery this banner offers is Back, not
                        Continue. */}
                    {chatUnavailable && (
                      <div
                        role="status"
                        data-testid="onboarding-guide-unavailable"
                        className="rounded-xl border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-muted-foreground"
                      >
                        <span className="font-medium text-foreground">Crewship Guide couldn&apos;t be reached right now.</span>{" "}
                        Nothing is lost: pick a template below and launch — you can talk to the Guide any time
                        afterwards from the dashboard, and add or change crews there.
                      </div>
                    )}
                    {chatNeedsCredential && !chatUnavailable && (
                      <div className="rounded-xl border border-warn/30 bg-warn/5 p-3 text-xs leading-relaxed text-muted-foreground">
                        Crewship Guide needs a model token before it can chat. Pick a template for
                        now, or go back to step 2 to add one and come back to talk it through.
                      </div>
                    )}
                    <div className="space-y-2">
                      {CREW_OPTIONS.map((opt, i) => {
                        const tpl = TEMPLATES[opt.slug]
                        const active = crewSlug === opt.slug
                        const Icon = tpl.Icon
                        return (
                          <motion.button
                            key={opt.slug}
                            type="button"
                            aria-pressed={active}
                            onClick={() => setCrewSlug(opt.slug)}
                            initial={reduce ? { opacity: 0 } : { opacity: 0, y: 6 }}
                            animate={{ opacity: 1, y: 0 }}
                            transition={{ duration: 0.35, ease, delay: i * 0.04 }}
                            whileHover={reduce ? undefined : { y: -1 }}
                            whileTap={{ scale: 0.99 }}
                            className={`flex w-full items-center gap-3 rounded-2xl border p-3.5 text-left transition-colors ${
                              active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
                            }`}
                          >
                            <span
                              className="w-10 h-10 rounded-xl flex items-center justify-center shrink-0"
                              style={{
                                backgroundColor: tpl.iconBg,
                                borderColor: tpl.iconBorder,
                                borderWidth: 1,
                              }}
                            >
                              <Icon className="h-5 w-5" style={{ color: tpl.iconColor }} />
                            </span>
                            <span className="text-sm font-medium flex-1 tracking-tight">{opt.label}</span>
                            <span className="text-xs text-muted-foreground tabular-nums">
                              {tpl.agents.length} {tpl.agents.length === 1 ? "agent" : "agents"}
                            </span>
                          </motion.button>
                        )
                      })}
                    </div>
                    {/* Not shown once the setup agent has already been ruled
                        out for this session (PRD §4.3's fallback-with-reason)
                        — offering a way back to a pane that will just fail
                        again is worse than not offering it. */}
                    {!chatUnavailable && (
                      <button
                        type="button"
                        onClick={() => {
                          // Clear the credential banner so a retry starts
                          // clean — OnboardingSetupChat remounts fresh on
                          // this switch and will re-evaluate the precondition
                          // itself; this only resets the PARENT's leftover
                          // "why we left" note from the last attempt.
                          setChatNeedsCredential(false)
                          setCrewMode("chat")
                        }}
                        className="text-xs font-medium text-primary underline-offset-2 hover:underline"
                      >
                        ← Talk to Crewship Guide instead
                      </button>
                    )}
                  </div>
                )}

                {!launchSummary && step === 2 && (
                  <div className="space-y-5">
                    <div>
                      {/* The heading asked "How will you work?" — the human's
                          question — while the step's actual requirement is
                          the agents' one. That framing is why the token could
                          look optional once you had answered about yourself.
                          It leads with what the crew needs now. */}
                      <h2 className="text-2xl font-semibold tracking-tight">
                        Give your agents a model
                      </h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        {isLocalModel(model)
                          ? "Your local model runs on your own endpoint, so no token is needed — just confirm the setup below."
                          : "Agents run in containers and need their own token to call a model. Pairing your CLI signs your terminal in; it does not give them one."}
                      </p>
                    </div>

                    {/* MODE PICKER. Browser first and Recommended, which is a
                        reversal: CLI led, on the reasoning that Claude Code
                        users already have a terminal open. That holds for
                        people who already run Crewship — not for the person
                        this screen is actually for, who is installing it for
                        the first time and does not have the CLI yet. Leading
                        with "Pair my CLI" sent them to a GitHub release page
                        to download a binary before they could finish signing
                        up, and the block underneath still says so.

                        Neither card changes what the agents need. The model
                        and its token are asked for below in both cases; this
                        choice is only about where the human works. */}
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                      <ModeCard
                        icon={Globe}
                        title="Chat in browser"
                        description="Nothing to install."
                        active={mode === "browser"}
                        recommended
                        onClick={() => setMode("browser")}
                      />
                      <ModeCard
                        icon={Terminal}
                        title="Also pair my CLI"
                        description="Optional — signs your terminal in too."
                        active={mode === "cli"}
                        onClick={() => setMode("cli")}
                      />
                    </div>

                    {/* CLI PAIRING BLOCK (only when CLI mode active) */}
                    <AnimatePresence>
                      {mode === "cli" && (
                        <motion.div
                          key="cli-pair"
                          initial={reduce ? { opacity: 0 } : { opacity: 0, y: 8 }}
                          animate={{ opacity: 1, y: 0 }}
                          exit={{ opacity: 0 }}
                          transition={{ duration: 0.3, ease }}
                          className="space-y-2"
                        >
                          <div className="text-xs text-muted-foreground leading-relaxed">
                            Don&apos;t have the Crewship CLI yet? Download from{" "}
                            <a
                              href="https://github.com/crewship-ai/crewship/releases"
                              target="_blank"
                              rel="noopener noreferrer"
                              className="text-primary underline-offset-2 hover:underline"
                            >
                              GitHub releases
                            </a>
                            , then run this on the machine where it lives:
                          </div>
                          {pairCode ? (
                            <>
                              <div className="flex items-center justify-between gap-2 rounded-xl border border-border bg-card p-3 font-mono text-xs shadow-sm">
                                <code className="text-success break-all leading-snug select-all">
                                  $ {pairCommand}
                                </code>
                                <Button
                                  type="button"
                                  variant="ghost"
                                  size="sm"
                                  onClick={copyPairCmd}
                                  className="shrink-0 h-7 w-7 p-0"
                                  aria-label="Copy command"
                                >
                                  <AnimatePresence mode="wait" initial={false}>
                                    {pairCopied ? (
                                      <motion.span
                                        key="check"
                                        initial={{ scale: 0.6, opacity: 0 }}
                                        animate={{ scale: 1, opacity: 1 }}
                                        exit={{ scale: 0.6, opacity: 0 }}
                                        transition={{ duration: 0.2 }}
                                      >
                                        <Check className="h-3.5 w-3.5 text-success" />
                                      </motion.span>
                                    ) : (
                                      <motion.span
                                        key="copy"
                                        initial={{ scale: 0.6, opacity: 0 }}
                                        animate={{ scale: 1, opacity: 1 }}
                                        exit={{ scale: 0.6, opacity: 0 }}
                                        transition={{ duration: 0.2 }}
                                      >
                                        <Copy className="h-3.5 w-3.5" />
                                      </motion.span>
                                    )}
                                  </AnimatePresence>
                                </Button>
                              </div>
                              {pairStatus === "pending" && (
                                <div className="flex items-center justify-between text-xs">
                                  <div className="flex items-center gap-2 text-warn">
                                    <span className="relative inline-flex h-2 w-2">
                                      <span className="absolute inset-0 rounded-full bg-warn animate-ping opacity-75" />
                                      <span className="relative inline-block h-2 w-2 rounded-full bg-warn" />
                                    </span>
                                    Waiting for your CLI…
                                  </div>
                                  {pairRemainingSec !== null && (
                                    <div
                                      className={`tabular-nums font-mono ${
                                        pairRemainingSec < 60
                                          ? "text-warn"
                                          : "text-muted-foreground"
                                      }`}
                                    >
                                      {formatCountdown(pairRemainingSec)}
                                    </div>
                                  )}
                                </div>
                              )}
                              {pairStatus === "consumed" && (
                                <motion.div
                                  initial={{ opacity: 0, scale: 0.96 }}
                                  animate={{ opacity: 1, scale: 1 }}
                                  transition={{ duration: 0.35, ease }}
                                  // items-start + a single <span> for the
                                  // sentence: with items-center and bare text
                                  // nodes, flex made each fragment around the
                                  // inline <code> its own column and the line
                                  // wrapped into an unreadable three-column
                                  // jumble at this width.
                                  className="flex items-start gap-2 rounded-lg border border-success/30 bg-success/5 px-3 py-2 text-xs text-success"
                                >
                                  <Check className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                                  <span className="leading-relaxed">
                                    {tokenDelivered ? (
                                      <>
                                        <strong className="font-semibold">CLI paired and model token received.</strong>{" "}
                                        Your crew is ready to launch.
                                      </>
                                    ) : (
                                      <>
                                        <strong className="font-semibold">CLI paired.</strong> Your terminal is
                                        signed in — the agents still need their own model token.
                                      </>
                                    )}
                                  </span>
                                </motion.div>
                              )}
                              {pairStatus === "expired" && (
                                <div className="flex items-center gap-2 text-xs text-destructive">
                                  <AlertTriangle className="h-3.5 w-3.5" />
                                  Code expired —{" "}
                                  <button
                                    type="button"
                                    className="underline font-medium"
                                    onClick={() => {
                                      // startPairing flips to "starting"
                                      // synchronously before its await, so
                                      // we don't have to bridge through
                                      // "idle" here — that would briefly
                                      // open the auto-start race window.
                                      setPairCode(null)
                                      setPairExpiresAt(null)
                                      void startPairing()
                                    }}
                                  >
                                    get a new one
                                  </button>
                                  .
                                </div>
                              )}
                            </>
                          ) : pairStatus === "failed" ? (
                            <div className="flex items-center justify-between gap-2 rounded-xl border border-destructive/30 bg-destructive/5 p-3 text-xs">
                              <div className="flex items-center gap-2 text-destructive">
                                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                                <span>Couldn&apos;t start pairing. Check your connection and try again.</span>
                              </div>
                              <button
                                type="button"
                                onClick={() => void startPairing()}
                                className="text-xs font-medium underline underline-offset-2 hover:text-foreground shrink-0"
                              >
                                Retry
                              </button>
                            </div>
                          ) : (
                            <div className="flex items-center gap-2 text-xs text-muted-foreground">
                              <Spinner className="h-3.5 w-3.5" /> Generating code…
                            </div>
                          )}
                        </motion.div>
                      )}
                    </AnimatePresence>

                    {/* The credential block below is NOT collapsed in CLI
                        mode any more, and this is the correction that matters
                        most on this screen.

                        Collapsing it hid the model picker, because the picker
                        lives inside it — so choosing "Pair my CLI" silently
                        took away the choice of which model the agents run.
                        That is backwards: the model and its token are facts
                        about the AGENTS. How the human drives Crewship is a
                        separate question, and the server says so in its own
                        comment — pairing_mode "drives how the human works,
                        not the agents".

                        Nothing here is optional-by-mode. A person installing
                        Crewship for the first time may not have the CLI at
                        all, and must be able to finish in the browser without
                        being sent to a release page to download one. */}
                    {(savedCredentialSelected || tokenDelivered) && (
                      <div className="flex items-start gap-2 rounded-xl border border-success/30 bg-success/5 p-3">
                        <Check className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success" />
                        <p className="text-[11px] leading-relaxed text-muted-foreground">
                          <span className="font-medium text-foreground">
                            {savedCredentialSelected
                              ? "Your saved Anthropic credential will be reused."
                              : "Your CLI already handed over a token."}
                          </span>{" "}
                          {savedCredentialSelected
                            ? "The secret stays encrypted and is not sent back to this page. Enter a new token only to replace it."
                            : "Change it only if you want the agents on a different credential."}
                        </p>
                      </div>
                    )}

                    {/* ADAPTER + MODEL + TOKEN. Always rendered, in both
                        modes: this is the question about the agents, and it
                        has the same answer however the human drives Crewship.
                        It used to be gated on `mode === "browser" ||
                        showCredential`, which is what hid the model picker
                        behind the CLI choice. */}
                    <div className="space-y-2">
                      <Label>Agent toolchain</Label>
                      <ToolchainPicker
                        value={adapter}
                        onChange={(key) => {
                          setAdapter(key)
                          setModel(CLI_ADAPTERS[key].defaultModel)
                        }}
                      />
                      {CLI_ADAPTERS[adapter]?.status !== "production" && (
                        <div role="alert" className="rounded-lg border border-warn/30 bg-warn/5 p-2.5 text-[11px] leading-relaxed text-muted-foreground">
                          {CLI_ADAPTERS[adapter]?.label} is still experimental and its CLI is not guaranteed to be present in the onboarding image. Choose Claude Code to finish setup; you can add experimental adapters from the dashboard afterwards.
                        </div>
                      )}
                      {/* The container-runtime precondition. Mirrors the
                          experimental-adapter alert above deliberately: same
                          shape, same rule — fail at the choice with an
                          explanation, rather than two steps later as an error
                          naming an internal component. */}
                      {runtimeInUse !== true && (
                        <div
                          role="alert"
                          data-testid="onboarding-runtime-blocker"
                          className="space-y-2 rounded-lg border border-warn/30 bg-warn/5 p-2.5 text-[11px] leading-relaxed text-muted-foreground"
                        >
                          {runtimeInUse === null || runtimeChecking ? (
                            <span>Checking for a container runtime…</span>
                          ) : (
                            <>
                              <div>
                                {/* Two different failures, and the fix differs, so
                                    they must not share a sentence. Docker absent:
                                    install/start it. Docker present but this server
                                    not driving it: crewshipd was started without a
                                    runtime and restarting it is the fix, which
                                    "start Docker" would never lead anyone to. */}
                                {runtimeReady
                                  ? "Docker is running, but this Crewship server isn't using it — it was started without a container runtime. Restart the server so it picks Docker up."
                                  : "Your agents run in Docker containers, and no container runtime is reachable. Install or start Docker, then re-check."}
                              </div>
                              <button
                                type="button"
                                onClick={() => void checkRuntime()}
                                disabled={runtimeChecking}
                                className="font-medium text-primary underline-offset-2 hover:underline disabled:opacity-60"
                              >
                                Re-check
                              </button>
                            </>
                          )}
                        </div>
                      )}
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="model">Model</Label>
                      <Select value={model} onValueChange={setModel}>
                        <SelectTrigger id="model" className="font-mono text-xs h-10">
                          <SelectValue placeholder="Select model" />
                        </SelectTrigger>
                        <SelectContent>
                          {getModelsForAdapter(adapter).map((m) => (
                            <SelectItem key={m.value} value={m.value} className="font-mono text-xs">
                              {m.label}
                              {m.value === adapterCfg?.defaultModel ? " · recommended" : ""}
                              {m.category === "legacy" ? " · older" : ""}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      {/* Stacks on a phone: "Claude Code CLI token" beside
                          "How to generate a Claude Code CLI token ↗" squeezes
                          both into two and three wrapped lines at 390px and
                          they read as one run-on. */}
                      <div className="flex flex-col items-start gap-1 sm:flex-row sm:items-center sm:justify-between sm:gap-2">
                        <Label htmlFor="api_key">{adapterCfg?.label ?? "Adapter"} CLI token</Label>
                        {ADAPTER_TOKEN_GUIDE[adapter] && (
                          <a
                            href={ADAPTER_TOKEN_GUIDE[adapter].url}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="text-[11px] text-primary inline-flex items-center gap-0.5 hover:underline"
                          >
                            {ADAPTER_TOKEN_GUIDE[adapter].label}
                            <ExternalLink className="h-2.5 w-2.5" />
                          </a>
                        )}
                      </div>
                      <div className="relative">
                        <Input
                          id="api_key"
                          type={showApiKey ? "text" : "password"}
                          value={apiKey}
                          onChange={(e) => setApiKey(e.target.value)}
                          placeholder={savedCredentialSelected ? "Saved token — leave blank to reuse" : "Paste your CLI token (starts with sk-ant-oat…)"}
                          autoComplete="off"
                          spellCheck={false}
                          className="font-mono text-xs h-10 pr-10"
                        />
                        <button
                          type="button"
                          onClick={() => setShowApiKey((v) => !v)}
                          aria-label={showApiKey ? "Hide token" : "Show token"}
                          aria-pressed={showApiKey}
                          className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                        >
                          {showApiKey ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
                        </button>
                      </div>
                      {/* Says what the pasted value IS before Continue asks
                          the provider. The one mistake this field exists to
                          catch — an sk-ant-api… console key where a CLI token
                          belongs — used to be reported only after a round
                          trip, as a generic "could not verify" error. */}
                      {tokenHint && (
                        <div
                          role={tokenHint.tone === "warn" ? "alert" : "status"}
                          data-testid="onboarding-token-hint"
                          className={`flex items-start gap-2 rounded-lg border px-3 py-2 text-[11px] leading-relaxed ${
                            tokenHint.tone === "warn"
                              ? "border-warn/30 bg-warn/5 text-warn"
                              : "border-success/30 bg-success/5 text-success"
                          }`}
                        >
                          {tokenHint.tone === "warn" ? (
                            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                          ) : (
                            <Check className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                          )}
                          <span>{tokenHint.text}</span>
                        </div>
                      )}
                      {isLocalModel(model) && (
                        <p className="text-[11px] text-muted-foreground leading-relaxed">
                          Local model selected — no API key needed. Leave this empty unless you also
                          want cloud models later. Requires{" "}
                          <code className="font-mono text-foreground/80">CREWSHIP_LOCAL_MODEL_BASE_URL</code>{" "}
                          on the server.
                        </p>
                      )}
                      {!isLocalModel(model) && ADAPTER_TOKEN_CMD[adapter] && (
                        <CommandSnippet
                          command={ADAPTER_TOKEN_CMD[adapter]}
                          caption="Run this on your machine, then paste the output above:"
                        />
                      )}
                      {!isLocalModel(model) && (
                        <p className="text-[11px] text-muted-foreground leading-relaxed">
                          This is the{" "}
                          <strong className="text-foreground/80">CLI token</strong>
                          {" "}from <code className="font-mono text-foreground/80">{ADAPTER_TOKEN_CMD[adapter] ?? "<cli> setup-token"}</code>,{" "}
                          <em>not</em> the raw account API key from the provider's console. The agents use the
                          CLI token via the same OAuth flow your local CLI does — pasting an sk-ant-api… key
                          here won&apos;t work.
                        </p>
                      )}
                    </div>

                    {/* Adapter install hint — only relevant in CLI mode */}
                    {mode === "cli" && ADAPTER_CLI_INSTALL[adapter] && (
                      <div className="rounded-xl border border-primary/20 bg-primary/5 p-3 text-xs flex items-start gap-2">
                        <Sparkles className="h-3.5 w-3.5 text-primary shrink-0 mt-0.5" />
                        <div className="flex-1 leading-relaxed text-muted-foreground">
                          New to {adapterCfg?.label}?{" "}
                          <a
                            href={ADAPTER_CLI_INSTALL[adapter].url}
                            target="_blank"
                            rel="noopener noreferrer"
                            className="text-primary hover:underline inline-flex items-center gap-0.5"
                          >
                            {ADAPTER_CLI_INSTALL[adapter].label}
                            <ExternalLink className="h-2.5 w-2.5" />
                          </a>
                          {" — "}then come back and paste the snippet above.
                        </div>
                      </div>
                    )}

                    {/* TELEMETRY CONSENT — explicit choice, pre-ticked to
                        the build's default (prerelease/dev = on, stable
                        = off; seeded from /api/v1/system/telemetry). The
                        answer is sticky server-side, same as running
                        `crewship telemetry on|off`. */}
                    <label
                      htmlFor="telemetry_opt_in"
                      className="flex items-start gap-2.5 rounded-xl border border-border p-3.5 cursor-pointer hover:bg-muted/40 transition-colors"
                    >
                      <Checkbox
                        id="telemetry_opt_in"
                        checked={telemetryOptIn}
                        onCheckedChange={(v) => setTelemetryOptIn(v === true)}
                        className="mt-0.5"
                      />
                      <span className="min-w-0">
                        <span className="block text-sm font-medium tracking-tight">
                          Send anonymous crash reports
                        </span>
                        <span className="block text-xs text-muted-foreground leading-relaxed mt-0.5">
                          Helps the maintainer fix bugs. Stack traces and version info only — never your
                          workspace data, credentials, or prompts. Change anytime with{" "}
                          <code className="font-mono text-foreground/80">crewship telemetry on|off</code>.
                        </span>
                      </span>
                    </label>
                  </div>
                )}
              </motion.div>
            </AnimatePresence>

            {error && (
              <motion.div
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                className="rounded-xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
                role="alert"
                aria-live="assertive"
              >
                {error}
              </motion.div>
            )}

            {!launchSummary && (
            /* Pinned to the foot of the column. Step 3 has no ceiling on its
               height — one card per crew the Guide creates, plus a row per
               crew/routine/page in "Built so far" — and Launch used to be
               pushed below the fold by the second crew, with nothing on
               screen hinting that the button finishing setup was down there.
               `sticky` rather than `fixed`: under lg the panes stack, and a
               fixed bar would stay welded to the viewport while the user
               scrolled on into the preview pane, offering controls for a
               screen they had already left. */
            <div className="sticky bottom-0 z-10 flex flex-wrap items-center justify-between gap-y-2 border-t border-border bg-background pb-6 pt-3 lg:pb-8">
              {blockingReason() && !submitting && !persistingCredential && !persistingWorkspace && (
                <div
                  data-testid="onboarding-blocking-reason"
                  aria-live="polite"
                  className="basis-full text-right text-[11px] text-muted-foreground"
                >
                  {blockingReason()}
                </div>
              )}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setStep((s) => (s > 1 ? ((s - 1) as Step) : s))}
                // Lock Back/Skip while Launch or the Adapter step's
                // credential persist is in flight — otherwise the user can
                // step back mid-submit or fire /complete while /setup or
                // POST /credentials is still running, which races those
                // endpoints against each other.
                disabled={step === 1 || submitting || persistingCredential || persistingWorkspace}
                className={step === 1 ? "invisible" : ""}
              >
                <ArrowLeft className="mr-2 h-4 w-4" />
                Back
              </Button>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => setSkipDialogOpen(true)}
                  disabled={submitting || persistingCredential || persistingWorkspace}
                  className="text-muted-foreground"
                >
                  Skip setup
                </Button>
                <AlertDialog open={skipDialogOpen} onOpenChange={setSkipDialogOpen}>
                  <AlertDialogContent>
                    <AlertDialogHeader>
                      <AlertDialogTitle>Skip setup?</AlertDialogTitle>
                      <AlertDialogDescription>
                        You will land on an empty dashboard with no crew and no model token, and this wizard
                        does not open again. Everything can still be added from the dashboard — a checklist
                        there will walk you through it — but nothing will work until you do.
                      </AlertDialogDescription>
                    </AlertDialogHeader>
                    <AlertDialogFooter>
                      <AlertDialogCancel>Keep going</AlertDialogCancel>
                      <AlertDialogAction
                        onClick={() => {
                          setSkipDialogOpen(false)
                          void handleSkip()
                        }}
                      >
                        Skip anyway
                      </AlertDialogAction>
                    </AlertDialogFooter>
                  </AlertDialogContent>
                </AlertDialog>
                {step < 3 ? (
                  <Button onClick={() => void handleContinue()} disabled={!canContinue() || submitting || persistingCredential || persistingWorkspace}>
                    {persistingCredential || persistingWorkspace ? <Spinner className="mr-2 h-4 w-4" /> : null}
                    {persistingCredential ? "Verifying token…" : "Continue"}
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </Button>
                ) : (
                  <Button onClick={handleLaunch} disabled={!canContinue() || submitting}>
                    {submitting ? (
                      <Spinner className="mr-2 h-4 w-4" />
                    ) : (
                      <Rocket className="mr-2 h-4 w-4" />
                    )}
                    Launch
                  </Button>
                )}
              </div>
            </div>
            )}

            {launchSummary && (
              /* pb-6 because the column no longer pads its own bottom — the
                 nav row owns that inset, and the nav row is gone on this
                 screen. Without it the receipt's last button sits flush
                 against the edge of the pane. */
              <div className="space-y-5 pb-6 lg:pb-8">
                <div>
                  <h2 className="text-2xl font-semibold tracking-tight">
                    {/* Zero is a real case, not a guard against one. Only
                        proposals the Guide made land in createdCrews; the
                        template path creates its crew inside POST
                        /onboarding/setup and never reports a roster back, so
                        this screen used to greet those users with
                        "Your 0 crews are ready" over an empty list. */}
                    {createdCrews.length <= 1 ? "Your crew is ready" : `Your ${createdCrews.length} crews are ready`}
                  </h2>
                  <p className="mt-1 text-sm text-muted-foreground">
                    {createdCrews.length === 0
                      ? "Setup is complete — your crew is deployed and ready to talk to."
                      : "Here is what Crewship just built for you."}
                  </p>
                </div>

                <ul className="space-y-3">
                  {createdCrews.map((c) => (
                    <li key={c.id} className="rounded-xl border border-border bg-card/60 p-3">
                      <div className="flex items-center justify-between gap-3">
                        <span className="flex min-w-0 items-center gap-2 font-medium">
                          {c.proposal.crewIcon && (
                            <CrewIcon icon={c.proposal.crewIcon} color={c.proposal.crewColor} size="sm" />
                          )}
                          <span className="truncate">{c.proposal.crewName}</span>
                        </span>
                        <span className="shrink-0 text-xs text-muted-foreground">
                          {c.proposal.agents.length}{" "}
                          {c.proposal.agents.length === 1 ? "agent" : "agents"}
                        </span>
                      </div>
                      <ul className="mt-2 space-y-1">
                        {c.proposal.agents.map((a) => (
                          <li key={a.name} className="flex items-baseline justify-between gap-3 text-xs">
                            <span className="text-muted-foreground">
                              <span className="text-foreground">{a.name}</span>
                              {a.role ? ` — ${a.role}` : ""}
                            </span>
                            <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{a.model}</span>
                          </li>
                        ))}
                      </ul>
                    </li>
                  ))}
                </ul>

                <div className="flex items-center gap-2 pt-1">
                  <Button
                    onClick={() =>
                      router.push(
                        launchSummary.agentSlug
                          ? `/chat/${encodeURIComponent(launchSummary.agentSlug)}`
                          : "/",
                      )
                    }
                  >
                    {launchSummary.agentSlug ? "Start chatting" : "Go to dashboard"}
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </Button>
                  {launchSummary.agentSlug && (
                    <Button variant="ghost" onClick={() => router.push("/")}>
                      Go to dashboard
                    </Button>
                  )}
                </div>
              </div>
            )}
          </div>
        </div>

        {/* RIGHT: live preview.
            A surface of its own, lit rather than decorated. `bg-muted/20` was
            within a hair of the left column's background, so the split read as
            one page with a hairline down it rather than as two panes.

            No mark here on purpose: it already sits in the lockup a few
            centimetres to the left, and a second copy at watermark opacity
            earns nothing except somewhere else for the eye to go. The cards
            are the only figure on this surface.

            Top-aligned for the same reason the left column is: the preview
            grows downward as you fill things in, and centring made the
            workspace card drift while it did. */}
        <div className="onboarding-pane relative flex items-start min-h-0 overflow-hidden p-6 lg:h-screen lg:p-12">
          {/* Step 3 in chat mode: the right panel becomes a chat with the
              setup agent (PRD §4.1/§1) instead of the static preview.
              Every other step, and step 3's template escape hatch, keep the
              live preview exactly as before — zero regression on the path
              that already works. */}
          {step === 3 && crewMode === "chat" ? (
            <OnboardingSetupChat
              onUnavailable={handleSetupAgentUnavailable}
              onProposalApplied={handleProposalApplied}
              onProposalPrepared={setPreparedProposal}
              language={language}
            />
          ) : (
            <OnboardingPreview
              workspaceName={workspaceName}
              crewSlug={crewSlug}
              mode={step === 2 ? mode : null}
              pairingPending={mode === "cli" && pairStatus !== "consumed"}
              adapterKey={adapter}
              model={model}
            />
          )}
        </div>
      </div>
    </div>
  )
}

function VerticalStepper({ step }: { step: Step }) {
  // Every step is one thing the person is deciding about, so each has the
  // icon of that thing — not a bare digit. "Model", not "Adapter": nobody
  // installing Crewship for the first time knows what an adapter is, and the
  // step's own heading already says what it is for.
  const items = [
    { n: 1, label: "Workspace", hint: "Name and agent language", Icon: Building2 },
    { n: 2, label: "Model", hint: "Toolchain and CLI token", Icon: Cpu },
    { n: 3, label: "Crew", hint: "Tell the Guide what you need", Icon: Users },
  ] as const
  return (
    // A row on a phone, a column from `sm` up. Stacked, the three rows plus
    // their hints cost the first screen ~130px of the space the actual form
    // needs; in a row the same information is one line.
    <ol className="flex items-start gap-4 sm:block sm:space-y-0" aria-label="Setup steps">
      {items.map((it, i) => {
        const active = step === it.n
        const done = step > it.n
        const Icon = it.Icon
        return (
          <li key={it.n} aria-current={active ? "step" : undefined}>
            <div className="flex items-center gap-3 text-sm">
              <motion.div
                animate={{
                  scale: active ? 1.05 : 1,
                  backgroundColor: done
                    ? "var(--color-primary)"
                    : active
                      ? "rgba(30,123,254,0.10)"
                      : "var(--color-muted)",
                }}
                transition={{ duration: 0.25, ease }}
                className={`flex h-8 w-8 items-center justify-center rounded-xl text-xs font-medium ${
                  done
                    ? "text-primary-foreground"
                    : active
                      ? "text-primary border border-primary/60"
                      : "text-muted-foreground"
                }`}
              >
                {done ? <Check className="h-4 w-4" /> : <Icon className="h-4 w-4" />}
              </motion.div>
              <div className="min-w-0 leading-tight">
                <div className={active || done ? "font-medium tracking-tight" : "text-muted-foreground"}>
                  <span className="mr-1.5 hidden font-mono text-[10px] text-muted-foreground sm:inline">{it.n}</span>
                  {it.label}
                </div>
                {active && (
                  <motion.div
                    initial={{ opacity: 0, y: -2 }}
                    animate={{ opacity: 1, y: 0 }}
                    transition={{ duration: 0.25, ease }}
                    className="hidden text-[11px] text-muted-foreground sm:block"
                  >
                    {it.hint}
                  </motion.div>
                )}
              </div>
            </div>
            {i < items.length - 1 && <div className="ml-4 my-0.5 hidden h-3 w-px bg-border sm:block" />}
          </li>
        )
      })}
    </ol>
  )
}

function ModeCard({
  icon: Icon,
  title,
  description,
  active,
  recommended,
  onClick,
}: {
  icon: typeof Globe
  title: string
  description: string
  active: boolean
  recommended?: boolean
  onClick: () => void
}) {
  return (
    <motion.button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      whileTap={{ scale: 0.99 }}
      className={`relative flex flex-col gap-1 rounded-2xl border p-4 text-left transition-colors ${
        active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
      }`}
    >
      {recommended && (
        <span className="absolute top-2 right-2 inline-flex items-center gap-1 rounded-full bg-primary/15 border border-primary/30 px-2 py-0.5 text-[10px] font-semibold text-primary-hover uppercase tracking-[0.06em]">
          Recommended
        </span>
      )}
      <Icon className={`h-5 w-5 ${active ? "text-primary" : "text-muted-foreground"}`} />
      <div className="text-sm font-medium tracking-tight">{title}</div>
      <div className="text-xs text-muted-foreground">{description}</div>
    </motion.button>
  )
}

/**
 * Format a number of seconds as "m:ss" — used by the pair-code
 * countdown so the user has a concrete sense of how long their code
 * stays valid. Anything 60s+ shows as minutes:seconds; under a minute
 * still shows as 0:NN for visual consistency.
 */
function formatCountdown(sec: number): string {
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}:${s.toString().padStart(2, "0")}`
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
 * injects that string into every agent's system prompt; Claude
 * understands all of them natively, so we don't need a code-table
 * translation layer.
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
          className="flex h-11 w-full items-center justify-between rounded-md border border-border bg-background px-3 text-sm hover:border-ring transition-colors"
        >
          {selected ? (
            <span className="inline-flex items-center gap-2 truncate">
              <span className="text-base leading-none">{selected.flag}</span>
              <span className="truncate">{selected.name}</span>
              <span className="text-xs text-muted-foreground truncate">· {selected.native}</span>
            </span>
          ) : (
            <span className="text-muted-foreground">Select language…</span>
          )}
          <ChevronsUpDown className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-[--radix-popover-trigger-width] p-0" align="start">
        <Command
          filter={(itemValue, search) => {
            // itemValue is the English name we set on each CommandItem.
            // Match on English name, native name, and ISO code so a
            // user typing "cs", "Čeština", or "Czech" all find Czech.
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
                  className="text-sm"
                >
                  <span className="mr-2 text-base leading-none">{lang.flag}</span>
                  <span>{lang.name}</span>
                  <span className="ml-auto text-[11px] text-muted-foreground">{lang.native}</span>
                  {value === lang.name && <Check className="ml-2 h-3.5 w-3.5 text-primary" />}
                </CommandItem>
              ))}
            </CommandGroup>
          </CommandList>
        </Command>
      </PopoverContent>
    </Popover>
  )
}
