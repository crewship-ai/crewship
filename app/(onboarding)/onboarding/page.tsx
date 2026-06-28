"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import { ArrowRight, ArrowLeft, Rocket, Globe, Terminal, Copy, Check, ExternalLink, Sparkles, AlertTriangle, ChevronsUpDown } from "lucide-react"
import { Spinner } from "@/components/ui/spinner"
import { CrewshipLogoTile } from "@/components/branding/crewship-logo"
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
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelsForAdapter } from "@/lib/cli-adapters"
import { buildOnboardingSetupBody } from "@/lib/onboarding-setup"
import { getAdapterBrand, ADAPTER_TOKEN_GUIDE, ADAPTER_TOKEN_CMD, ADAPTER_CLI_INSTALL } from "@/lib/cli-adapter-brand"
import { LANGUAGES } from "@/lib/languages"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from "@/components/ui/command"
import {
  OnboardingPreview,
  TEMPLATES,
  type CrewTemplateSlug,
  type HandoffMode,
} from "@/components/features/onboarding/onboarding-preview"

/**
 * Variant D — split-screen onboarding. Left pane: form with vertical
 * stepper (Workspace → Crew → Adapter). Right pane: live preview that
 * animates as the user makes choices. On <lg breakpoints the preview
 * collapses below the form into a single column.
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
 * legacyCopy — fallback for non-secure contexts (HTTP dev hosts).
 * Renders a hidden textarea, selects it, and triggers the deprecated
 * but still-everywhere-supported document.execCommand('copy'). The
 * surrounding caller already gated on navigator.clipboard being
 * unavailable, so this path is only ever hit when there's no
 * better option.
 */
function legacyCopy(text: string, onSuccess: () => void) {
  if (typeof document === "undefined") return
  const ta = document.createElement("textarea")
  ta.value = text
  ta.setAttribute("readonly", "")
  ta.style.position = "fixed"
  ta.style.top = "-1000px"
  ta.style.opacity = "0"
  document.body.appendChild(ta)
  ta.select()
  ta.setSelectionRange(0, text.length)
  try {
    document.execCommand("copy")
    onSuccess()
  } catch {
    // No clipboard at all — silent. The code is visible on screen
    // so the user can still select-and-copy by hand.
  } finally {
    document.body.removeChild(ta)
  }
}

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

type Step = 1 | 2 | 3

export default function OnboardingPage() {
  const router = useRouter()
  const reduce = useReducedMotion()
  const [step, setStep] = useState<Step>(1)
  const [checking, setChecking] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [workspaceName, setWorkspaceName] = useState("")
  const [language, setLanguage] = useState<string>("English")
  const [crewSlug, setCrewSlug] = useState<CrewTemplateSlug | null>(null)
  // Default to "cli" — Claude Code users (our primary persona) almost
  // always have a local CLI already; flagging it as the recommended
  // path matters more than the browser-chat fallback.
  const [mode, setMode] = useState<HandoffMode>("cli")
  const [adapter, setAdapter] = useState<string>("CLAUDE_CODE")
  const [model, setModel] = useState<string>("")
  const [apiKey, setApiKey] = useState("")
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
  const [pairStatus, setPairStatus] = useState<"idle" | "starting" | "pending" | "consumed" | "expired" | "failed">("idle")
  const [pairCopied, setPairCopied] = useState(false)
  const [runtimeReady, setRuntimeReady] = useState<boolean | null>(null)

  // Already-onboarded gate
  useEffect(() => {
    fetch("/api/v1/onboarding/status")
      .then((r) => (r.ok ? r.json() : { completed: false }))
      .then((d) => {
        if (d.completed) {
          router.push("/")
          return
        }
        setChecking(false)
      })
      .catch(() => setChecking(false))
  }, [router])

  useEffect(() => {
    // Prefill workspace name from the signed-in user's display name as
    // a starting suggestion. Functional setter pattern lets the user
    // type into the input before /api/auth/session resolves without
    // having their typing overwritten — the setter sees the latest
    // committed value and only applies the prefill when it's empty.
    fetch("/api/auth/session")
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        const name = d?.user?.name || d?.user?.email
        if (!name) return
        const base = String(name).split("@")[0]
        setWorkspaceName((current) => (current ? current : `${base}'s Workspace`))
      })
      .catch(() => undefined)
  }, [])

  useEffect(() => {
    fetch("/api/v1/system/runtime")
      .then((r) => (r.ok ? r.json() : { available: false }))
      .then((d) => setRuntimeReady(Boolean(d.available)))
      .catch(() => setRuntimeReady(false))
  }, [])

  // Seed the telemetry consent checkbox from the server's current state:
  // prerelease/dev builds boot with crash reporting defaulted on, stable
  // builds default off (internal/crashreport.DefaultOptIn). On any fetch
  // failure the checkbox stays unticked — the privacy-preserving default.
  useEffect(() => {
    fetch("/api/v1/system/telemetry")
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
    if (mode !== "cli" || step !== 3 || !pairCode || pairStatus !== "pending") return
    const interval = setInterval(async () => {
      try {
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
      const res = await fetch("/api/v1/auth/pair/start", {
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
    // Auto-start pairing on first arrival at step 3 (CLI mode). Don't
    // retry on failure — the "failed" status surfaces a manual retry
    // button instead, so we don't hammer the server in a hot loop if
    // /pair/start is consistently rejecting.
    if (mode === "cli" && step === 3 && !pairCode && pairStatus === "idle") {
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
    const succeed = () => {
      setPairCopied(true)
      setTimeout(() => setPairCopied(false), 1500)
    }
    const modernAvailable =
      typeof navigator !== "undefined" &&
      navigator.clipboard &&
      typeof navigator.clipboard.writeText === "function" &&
      typeof window !== "undefined" &&
      window.isSecureContext
    if (modernAvailable) {
      navigator.clipboard.writeText(pairCommand).then(succeed).catch(() => legacyCopy(pairCommand, succeed))
      return
    }
    legacyCopy(pairCommand, succeed)
  }, [pairCommand])

  /**
   * Step 3 validation — API key is required in BOTH modes because the
   * agents in containers always need a provider credential to call
   * Claude (the CLI token is for the user's terminal, not the agents).
   * In CLI mode we ALSO require the pair to be consumed so the local
   * CLI is ready to drive the workspace.
   */
  const canContinue = () => {
    if (step === 1) return workspaceName.trim().length >= 2
    if (step === 2) return crewSlug !== null
    if (step === 3) {
      const keyOK = apiKey.trim().length >= 8
      if (mode === "browser") return keyOK
      if (mode === "cli") return keyOK && pairStatus === "consumed"
    }
    return false
  }

  async function handleLaunch() {
    setSubmitting(true)
    setError(null)
    try {
      const adapterCfg = CLI_ADAPTERS[adapter]
      const body = buildOnboardingSetupBody({
        workspaceName,
        language,
        crewSlug,
        adapter,
        adapterLabel: adapterCfg?.label,
        provider: adapterCfg?.provider,
        envVar: adapterCfg?.envVar,
        model,
        apiKey,
        pairingMode: mode === "cli",
        telemetryOptIn,
      })
      const res = await fetch("/api/v1/onboarding/setup", {
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
        }
      } catch {
        // localStorage unavailable (private mode) — skip the breadcrumb,
        // dashboard will just not show the banner. Not worth blocking
        // onboarding completion on.
      }
      if (data.agent_id) {
        router.push(`/crews/agents/${data.agent_id}/chat`)
      } else {
        router.push("/")
      }
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
    try {
      await fetch("/api/v1/onboarding/complete", { method: "POST" })
    } catch {
      // ignore
    }
    router.push("/")
  }

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center bg-background">
        <Spinner className="h-8 w-8 text-muted-foreground" />
      </div>
    )
  }

  const adapterCfg = CLI_ADAPTERS[adapter]

  return (
    <div className="min-h-screen bg-background">
      {/* Subtle hero glow — same radial gradient idea from
          crewship-web's .hero-glow but anchored to the top of the
          form column so the form has a sense of stage lighting
          without distracting from the live preview. */}
      <div className="pointer-events-none absolute inset-x-0 top-0 h-[360px] bg-[radial-gradient(ellipse_60%_50%_at_30%_0%,rgba(30,123,254,0.10),transparent_60%)]" />

      <div className="grid grid-cols-1 lg:grid-cols-2 min-h-screen relative">
        {/* LEFT: form */}
        <div className="border-b lg:border-b-0 lg:border-r border-border p-6 lg:p-12 flex items-center">
          <div className="w-full max-w-md mx-auto space-y-7">
            <motion.div
              initial={reduce ? { opacity: 0 } : { opacity: 0, y: -8 }}
              animate={{ opacity: 1, y: 0 }}
              transition={{ duration: 0.45, ease }}
              className="flex items-center gap-3"
            >
              <CrewshipLogoTile size="h-10 w-10" iconSize="h-5 w-5" rounded="rounded-xl" />
              <div>
                <div className="text-[11px] uppercase tracking-[0.18em] text-muted-foreground">Crewship</div>
                <h1 className="text-lg font-semibold tracking-tight">Setup</h1>
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
                {step === 1 && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">What&apos;s your workspace called?</h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        A workspace holds your crews, agents, and credentials. You can rename it later.
                      </p>
                    </div>

                    {/* Upfront warning so users get the CLI token ready BEFORE
                        step 3 instead of bouncing back and forth. Copy-paste
                        cmd inline for the most common (Claude Code) case. */}
                    <div className="rounded-xl border border-amber-500/30 bg-amber-500/5 p-3 text-xs leading-relaxed">
                      <div className="flex items-start gap-2">
                        <AlertTriangle className="h-4 w-4 text-amber-500 shrink-0 mt-0.5" />
                        <div className="space-y-1.5 min-w-0">
                          <div className="text-foreground/90 font-medium">
                            Heads up — you&apos;ll need a CLI token in step 3
                          </div>
                          <div className="text-muted-foreground">
                            Crewship uses your provider&apos;s <strong className="text-foreground/80">CLI token</strong>,{" "}
                            <em>not</em> the account API key from their web console. Get it ready now:
                          </div>
                          <div className="rounded-md border border-border bg-card/60 p-2 font-mono mt-1.5">
                            <span className="text-muted-foreground">Claude Code:</span>{" "}
                            <span className="text-emerald-500 select-all">$ claude setup-token</span>
                          </div>
                          <div className="text-[10px] text-muted-foreground">
                            Other adapters (Gemini, Codex, Cursor, OpenCode, Factory) have their own
                            <code className="mx-1 font-mono">setup-token</code> equivalents — links in step 3.
                          </div>
                        </div>
                      </div>
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
                    {runtimeReady === true && (
                      <motion.div
                        initial={{ opacity: 0, x: -6 }}
                        animate={{ opacity: 1, x: 0 }}
                        transition={{ duration: 0.35, ease, delay: 0.15 }}
                        className="inline-flex items-center gap-2 text-xs text-emerald-500"
                      >
                        <Check className="h-3.5 w-3.5" /> Docker detected
                      </motion.div>
                    )}
                    {runtimeReady === false && (
                      <div className="rounded-xl border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-300">
                        Docker isn&apos;t reachable. You can still finish setup now and start a runtime later from
                        Settings.
                      </div>
                    )}
                  </div>
                )}

                {step === 2 && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">Pick your first crew</h2>
                      <p className="text-sm text-muted-foreground mt-1">Watch the preview build itself on the right.</p>
                    </div>
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
                  </div>
                )}

                {step === 3 && (
                  <div className="space-y-5">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">How will you work?</h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        Drive Crewship from your terminal or stay in the browser. Either way the agents need an
                        API key below to call their model.
                      </p>
                    </div>

                    {/* MODE PICKER — CLI first with Recommended badge,
                        because Claude Code users are our primary
                        persona and they already have a terminal open. */}
                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                      <ModeCard
                        icon={Terminal}
                        title="Pair my CLI"
                        description="Claude Code, Gemini, Codex, OpenCode…"
                        active={mode === "cli"}
                        recommended
                        onClick={() => setMode("cli")}
                      />
                      <ModeCard
                        icon={Globe}
                        title="Chat in browser"
                        description="No terminal required."
                        active={mode === "browser"}
                        onClick={() => setMode("browser")}
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
                                <code className="text-emerald-400 break-all leading-snug select-all">
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
                                        <Check className="h-3.5 w-3.5 text-emerald-500" />
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
                                  <div className="flex items-center gap-2 text-amber-500">
                                    <span className="relative inline-flex h-2 w-2">
                                      <span className="absolute inset-0 rounded-full bg-amber-500 animate-ping opacity-75" />
                                      <span className="relative inline-block h-2 w-2 rounded-full bg-amber-500" />
                                    </span>
                                    Waiting for your CLI…
                                  </div>
                                  {pairRemainingSec !== null && (
                                    <div
                                      className={`tabular-nums font-mono ${
                                        pairRemainingSec < 60
                                          ? "text-amber-500"
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
                                  className="flex items-center gap-2 text-xs text-emerald-500"
                                >
                                  <Check className="h-3.5 w-3.5" /> CLI paired. You can finish below or jump
                                  to <code className="font-mono">crewship setup</code> in the terminal.
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

                    {/* ADAPTER + API KEY — always visible because agents
                        need a credential regardless of mode. */}
                    <div className="space-y-2">
                      <Label>Agent toolchain</Label>
                      <div className="grid grid-cols-2 gap-2">
                        {CLI_ADAPTER_KEYS.map((key) => {
                          const cfg = CLI_ADAPTERS[key]
                          const Icon = cfg.icon
                          const brand = getAdapterBrand(key)
                          const active = adapter === key
                          return (
                            <motion.button
                              key={key}
                              type="button"
                              aria-pressed={active}
                              onClick={() => {
                                setAdapter(key)
                                setModel(cfg.defaultModel)
                              }}
                              whileTap={{ scale: 0.98 }}
                              className={`flex items-center gap-2 rounded-xl border p-2.5 text-left transition-colors ${
                                active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
                              }`}
                            >
                              <span
                                className="w-7 h-7 rounded-lg flex items-center justify-center shrink-0"
                                style={{
                                  backgroundColor: brand.bg,
                                  borderColor: brand.border,
                                  borderWidth: 1,
                                }}
                              >
                                <Icon className="h-3.5 w-3.5" style={{ color: brand.fg }} />
                              </span>
                              <span className="text-xs font-medium truncate">{cfg.label}</span>
                            </motion.button>
                          )
                        })}
                      </div>
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
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    <div className="space-y-2">
                      <div className="flex items-center justify-between">
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
                      <Input
                        id="api_key"
                        type="password"
                        value={apiKey}
                        onChange={(e) => setApiKey(e.target.value)}
                        placeholder="CLI token (not your account API key)"
                        className="font-mono text-xs h-10"
                      />
                      {ADAPTER_TOKEN_CMD[adapter] && (
                        <div className="rounded-md border border-border bg-muted/40 p-2.5 font-mono text-[11px] leading-relaxed">
                          <span className="text-muted-foreground">Run this on your machine, paste the output above:</span>
                          <div className="text-emerald-500 mt-1 select-all">$ {ADAPTER_TOKEN_CMD[adapter]}</div>
                        </div>
                      )}
                      <p className="text-[11px] text-muted-foreground leading-relaxed">
                        This is the{" "}
                        <strong className="text-foreground/80">CLI token</strong>
                        {" "}from <code className="font-mono text-foreground/80">{ADAPTER_TOKEN_CMD[adapter] ?? "<cli> setup-token"}</code>,{" "}
                        <em>not</em> the raw account API key from the provider's console. The agents use the
                        CLI token via the same OAuth flow your local CLI does — pasting an sk-ant-api… key
                        here won&apos;t work.
                      </p>
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

            <div className="flex items-center justify-between pt-1">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setStep((s) => (s > 1 ? ((s - 1) as Step) : s))}
                // Lock Back/Skip while Launch is in flight — otherwise
                // the user can step back mid-submit or fire /complete
                // while /setup is still running, which races the two
                // endpoints against each other.
                disabled={step === 1 || submitting}
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
                  onClick={handleSkip}
                  disabled={submitting}
                  className="text-muted-foreground"
                >
                  Skip setup
                </Button>
                {step < 3 ? (
                  <Button onClick={() => setStep((s) => (s + 1) as Step)} disabled={!canContinue() || submitting}>
                    Continue
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
          </div>
        </div>

        {/* RIGHT: live preview */}
        <div className="bg-muted/20 p-6 lg:p-12 flex items-center">
          <OnboardingPreview
            workspaceName={workspaceName}
            crewSlug={crewSlug}
            mode={step === 3 ? mode : null}
            pairingPending={mode === "cli" && pairStatus !== "consumed"}
            adapterKey={adapter}
          />
        </div>
      </div>
    </div>
  )
}

function VerticalStepper({ step }: { step: Step }) {
  const items = [
    { n: 1, label: "Workspace" },
    { n: 2, label: "Crew" },
    { n: 3, label: "Adapter" },
  ] as const
  return (
    <div className="space-y-0">
      {items.map((it, i) => {
        const active = step === it.n
        const done = step > it.n
        return (
          <div key={it.n}>
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
                className={`flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium ${
                  done
                    ? "text-primary-foreground"
                    : active
                      ? "text-primary border-2 border-primary"
                      : "text-muted-foreground"
                }`}
              >
                {done ? <Check className="h-3.5 w-3.5" /> : it.n}
              </motion.div>
              <span className={active || done ? "font-medium tracking-tight" : "text-muted-foreground"}>
                {it.label}
              </span>
            </div>
            {i < items.length - 1 && <div className="ml-3 w-px h-3 bg-border" />}
          </div>
        )
      })}
    </div>
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
