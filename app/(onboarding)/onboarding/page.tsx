"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import { motion, AnimatePresence, useReducedMotion } from "motion/react"
import { Loader2, ArrowRight, ArrowLeft, Rocket, Globe, Terminal, Copy, Check } from "lucide-react"
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
import { CLI_ADAPTERS, CLI_ADAPTER_KEYS, getModelsForAdapter } from "@/lib/cli-adapters"
import { getAdapterBrand } from "@/lib/cli-adapter-brand"
import {
  OnboardingPreview,
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

const CREW_OPTIONS: { slug: CrewTemplateSlug; label: string; emoji: string; color: string }[] = [
  { slug: "software-development", label: "Software Development", emoji: "\u{1F4BB}", color: "#5DA1FF" },
  { slug: "devops-sre", label: "DevOps / SRE", emoji: "\u{1F527}", color: "#F472B6" },
  { slug: "content-marketing", label: "Content Marketing", emoji: "\u{1F4E2}", color: "#C084FC" },
  { slug: "accounting-finance", label: "Accounting & Finance", emoji: "\u{1F9EE}", color: "#34D399" },
  { slug: "blank", label: "Start blank", emoji: "➕", color: "#A1A1AA" },
]

type Step = 1 | 2 | 3

export default function OnboardingPage() {
  const router = useRouter()
  const reduce = useReducedMotion()
  const [step, setStep] = useState<Step>(1)
  const [checking, setChecking] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const [workspaceName, setWorkspaceName] = useState("")
  const [crewSlug, setCrewSlug] = useState<CrewTemplateSlug | null>(null)
  const [mode, setMode] = useState<HandoffMode>("browser")
  const [adapter, setAdapter] = useState<string>("CLAUDE_CODE")
  const [model, setModel] = useState<string>("")
  const [apiKey, setApiKey] = useState("")

  const [pairCode, setPairCode] = useState<string | null>(null)
  const [pairExpiresAt, setPairExpiresAt] = useState<string | null>(null)
  const [pairStatus, setPairStatus] = useState<"idle" | "pending" | "consumed" | "expired">("idle")
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
    if (workspaceName) return
    fetch("/api/auth/session")
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        const name = d?.user?.name || d?.user?.email
        if (name && !workspaceName) {
          const base = String(name).split("@")[0]
          setWorkspaceName(`${base}'s Workspace`)
        }
      })
      .catch(() => undefined)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    fetch("/api/v1/system/runtime")
      .then((r) => (r.ok ? r.json() : { available: false }))
      .then((d) => setRuntimeReady(Boolean(d.available)))
      .catch(() => setRuntimeReady(false))
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

  const startPairing = useCallback(async () => {
    setError(null)
    try {
      const res = await fetch("/api/v1/auth/pair/start", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ adapter_hint: adapter }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error ?? "Could not start pairing")
        return
      }
      const data = await res.json()
      setPairCode(data.code)
      setPairExpiresAt(data.expires_at)
      setPairStatus("pending")
    } catch {
      setError("Network error starting pairing")
    }
  }, [adapter])

  useEffect(() => {
    if (mode === "cli" && step === 3 && !pairCode) {
      void startPairing()
    }
  }, [mode, step, pairCode, startPairing])

  const copyPairCmd = useCallback(() => {
    if (!pairCode) return
    const cmd = `crewship login --pair --code=${pairCode}`
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
      navigator.clipboard.writeText(cmd).then(succeed).catch(() => legacyCopy(cmd, succeed))
      return
    }
    legacyCopy(cmd, succeed)
  }, [pairCode])

  const canContinue = () => {
    if (step === 1) return workspaceName.trim().length >= 2
    if (step === 2) return crewSlug !== null
    if (step === 3) {
      if (mode === "browser") return apiKey.trim().length >= 8
      if (mode === "cli") return pairStatus === "consumed"
    }
    return false
  }

  async function handleLaunch() {
    setSubmitting(true)
    setError(null)
    try {
      const adapterCfg = CLI_ADAPTERS[adapter]
      const body: Record<string, unknown> = {
        workspace_name: workspaceName,
        crew_template_slug: crewSlug && crewSlug !== "blank" ? crewSlug : undefined,
        crew_name: crewSlug === "blank" ? "My Crew" : undefined,
        agent_name: crewSlug === "blank" ? `${adapterCfg?.label ?? "Agent"} #1` : undefined,
        cli_adapter: adapter,
        llm_provider: adapterCfg?.provider,
        llm_model: model || undefined,
        credential_name: adapterCfg?.envVar,
        credential_value: mode === "browser" ? apiKey : undefined,
        pairing_mode: mode === "cli",
      }
      const res = await fetch("/api/v1/onboarding/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        setError(data.error ?? "Onboarding failed")
        setSubmitting(false)
        return
      }
      const data = await res.json()
      if (data.agent_id) {
        router.push(`/crews/agents/${data.agent_id}/chat`)
      } else {
        router.push("/")
      }
    } catch {
      setError("Network error. Please try again.")
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
        <Loader2 className="h-8 w-8 animate-spin text-muted-foreground" />
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
                        const active = crewSlug === opt.slug
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
                              className="w-9 h-9 rounded-xl flex items-center justify-center text-lg shrink-0"
                              style={{
                                backgroundColor: `${opt.color}1F`,
                                borderColor: `${opt.color}66`,
                                borderWidth: 1,
                              }}
                            >
                              <span style={{ color: opt.color }}>{opt.emoji}</span>
                            </span>
                            <span className="text-sm font-medium flex-1 tracking-tight">{opt.label}</span>
                            <span className="text-xs text-muted-foreground tabular-nums">
                              {opt.slug === "blank" ? "1 agent" : "4 agents"}
                            </span>
                          </motion.button>
                        )
                      })}
                    </div>
                  </div>
                )}

                {step === 3 && (
                  <div className="space-y-4">
                    <div>
                      <h2 className="text-2xl font-semibold tracking-tight">How will you work?</h2>
                      <p className="text-sm text-muted-foreground mt-1">
                        Browser is the fastest start. Pair a local CLI to drive things from your own machine.
                      </p>
                    </div>

                    <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
                      <ModeCard
                        icon={Globe}
                        title="Chat in browser"
                        description="Quickest start. Paste an API key."
                        active={mode === "browser"}
                        onClick={() => setMode("browser")}
                      />
                      <ModeCard
                        icon={Terminal}
                        title="Pair local CLI"
                        description="Claude Code, Gemini, Codex…"
                        active={mode === "cli"}
                        onClick={() => setMode("cli")}
                      />
                    </div>

                    <AnimatePresence mode="wait">
                      {mode === "browser" && (
                        <motion.div
                          key="browser-config"
                          initial={reduce ? { opacity: 0 } : { opacity: 0, y: 8 }}
                          animate={{ opacity: 1, y: 0 }}
                          exit={{ opacity: 0 }}
                          transition={{ duration: 0.3, ease }}
                          className="space-y-3"
                        >
                          <div className="space-y-2">
                            <Label>CLI Adapter</Label>
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
                            <Label htmlFor="api_key">API key</Label>
                            <Input
                              id="api_key"
                              type="password"
                              value={apiKey}
                              onChange={(e) => setApiKey(e.target.value)}
                              placeholder={`${adapterCfg?.envVar ?? "API_KEY"} value`}
                              className="font-mono text-xs h-10"
                            />
                          </div>
                        </motion.div>
                      )}

                      {mode === "cli" && (
                        <motion.div
                          key="cli-config"
                          initial={reduce ? { opacity: 0 } : { opacity: 0, y: 8 }}
                          animate={{ opacity: 1, y: 0 }}
                          exit={{ opacity: 0 }}
                          transition={{ duration: 0.3, ease }}
                          className="space-y-3"
                        >
                          <div className="text-xs text-muted-foreground">
                            Run this on the machine where your CLI lives. Works with any of the six supported
                            adapters — the server doesn&apos;t care which.
                          </div>
                          {pairCode ? (
                            <>
                              <div className="flex items-center justify-between gap-2 rounded-xl border border-border bg-card p-3 font-mono text-xs shadow-sm">
                                <code className="text-emerald-400 break-all leading-snug select-all">
                                  $ crewship login --pair --code={pairCode}
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
                                <div className="flex items-center gap-2 text-xs text-amber-500">
                                  <span className="relative inline-flex h-2 w-2">
                                    <span className="absolute inset-0 rounded-full bg-amber-500 animate-ping opacity-75" />
                                    <span className="relative inline-block h-2 w-2 rounded-full bg-amber-500" />
                                  </span>
                                  Waiting for your CLI to connect…
                                </div>
                              )}
                              {pairStatus === "consumed" && (
                                <motion.div
                                  initial={{ opacity: 0, scale: 0.96 }}
                                  animate={{ opacity: 1, scale: 1 }}
                                  transition={{ duration: 0.35, ease }}
                                  className="flex items-center gap-2 text-xs text-emerald-500"
                                >
                                  <Check className="h-3.5 w-3.5" /> CLI paired. Ready to launch.
                                </motion.div>
                              )}
                              {pairStatus === "expired" && (
                                <div className="flex items-center gap-2 text-xs text-destructive">
                                  Code expired —{" "}
                                  <button
                                    type="button"
                                    className="underline"
                                    onClick={() => {
                                      setPairCode(null)
                                      setPairExpiresAt(null)
                                      setPairStatus("idle")
                                      void startPairing()
                                    }}
                                  >
                                    get a new one
                                  </button>
                                  .
                                </div>
                              )}
                              {pairExpiresAt && pairStatus === "pending" && (
                                <div className="text-[10px] text-muted-foreground tabular-nums">
                                  Expires at {new Date(pairExpiresAt).toLocaleTimeString()}.
                                </div>
                              )}
                            </>
                          ) : (
                            <div className="flex items-center gap-2 text-xs text-muted-foreground">
                              <Loader2 className="h-3.5 w-3.5 animate-spin" /> Generating code…
                            </div>
                          )}
                        </motion.div>
                      )}
                    </AnimatePresence>
                  </div>
                )}
              </motion.div>
            </AnimatePresence>

            {error && (
              <motion.div
                initial={{ opacity: 0, y: 6 }}
                animate={{ opacity: 1, y: 0 }}
                className="rounded-xl border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive"
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
                disabled={step === 1}
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
                  className="text-muted-foreground"
                >
                  Skip setup
                </Button>
                {step < 3 ? (
                  <Button onClick={() => setStep((s) => (s + 1) as Step)} disabled={!canContinue()}>
                    Continue
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </Button>
                ) : (
                  <Button onClick={handleLaunch} disabled={!canContinue() || submitting}>
                    {submitting ? (
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
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
  onClick,
}: {
  icon: typeof Globe
  title: string
  description: string
  active: boolean
  onClick: () => void
}) {
  return (
    <motion.button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      whileTap={{ scale: 0.99 }}
      className={`flex flex-col gap-1 rounded-2xl border p-4 text-left transition-colors ${
        active ? "border-primary bg-primary/5" : "border-border hover:bg-muted/50"
      }`}
    >
      <Icon className={`h-5 w-5 ${active ? "text-primary" : "text-muted-foreground"}`} />
      <div className="text-sm font-medium tracking-tight">{title}</div>
      <div className="text-xs text-muted-foreground">{description}</div>
    </motion.button>
  )
}
