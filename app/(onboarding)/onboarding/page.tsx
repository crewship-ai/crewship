"use client"

import { useState, useEffect, useCallback } from "react"
import { useRouter } from "next/navigation"
import { Ship, Loader2, ArrowRight, ArrowLeft, Rocket, Globe, Terminal, Copy, Check } from "lucide-react"
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
 */

const CREW_OPTIONS: { slug: CrewTemplateSlug; label: string; emoji: string }[] = [
  { slug: "software-development", label: "Software Development", emoji: "\u{1F4BB}" },
  { slug: "devops-sre", label: "DevOps / SRE", emoji: "\u{1F527}" },
  { slug: "content-marketing", label: "Content Marketing", emoji: "\u{1F4E2}" },
  { slug: "accounting-finance", label: "Accounting & Finance", emoji: "\u{1F9EE}" },
  { slug: "blank", label: "Start blank", emoji: "➕" },
]

type Step = 1 | 2 | 3

export default function OnboardingPage() {
  const router = useRouter()
  const [step, setStep] = useState<Step>(1)
  const [checking, setChecking] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Form state
  const [workspaceName, setWorkspaceName] = useState("")
  const [crewSlug, setCrewSlug] = useState<CrewTemplateSlug | null>(null)
  const [mode, setMode] = useState<HandoffMode>("browser")
  const [adapter, setAdapter] = useState<string>("CLAUDE_CODE")
  const [model, setModel] = useState<string>("")
  const [apiKey, setApiKey] = useState("")

  // Pairing flow state
  const [pairCode, setPairCode] = useState<string | null>(null)
  const [pairExpiresAt, setPairExpiresAt] = useState<string | null>(null)
  const [pairStatus, setPairStatus] = useState<"idle" | "pending" | "consumed" | "expired">("idle")
  const [pairCopied, setPairCopied] = useState(false)

  // Inline runtime check (no longer its own step — surfaced as badge in step 1)
  const [runtimeReady, setRuntimeReady] = useState<boolean | null>(null)

  // Onboarding-already-complete guard
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

  // Pre-fill workspace name from session
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

  // Runtime detection (best-effort)
  useEffect(() => {
    fetch("/api/v1/system/runtime")
      .then((r) => (r.ok ? r.json() : { available: false }))
      .then((d) => setRuntimeReady(Boolean(d.available)))
      .catch(() => setRuntimeReady(false))
  }, [])

  // Default model from adapter
  useEffect(() => {
    const cfg = CLI_ADAPTERS[adapter]
    if (cfg && !model) setModel(cfg.defaultModel)
  }, [adapter, model])

  // Pairing poll loop — only while step=3, mode=cli, code is live and pending
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

  // Kick off pairing when user enters step 3 with cli mode
  useEffect(() => {
    if (mode === "cli" && step === 3 && !pairCode) {
      void startPairing()
    }
  }, [mode, step, pairCode, startPairing])

  const copyPairCmd = useCallback(() => {
    if (!pairCode) return
    const cmd = `crewship login --pair --code=${pairCode}`
    void navigator.clipboard.writeText(cmd).then(() => {
      setPairCopied(true)
      setTimeout(() => setPairCopied(false), 1500)
    })
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
        // Blank path needs CrewName + AgentName the backend still demands.
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
      <div className="grid grid-cols-1 lg:grid-cols-2 min-h-screen">
        {/* LEFT: form */}
        <div className="border-b lg:border-b-0 lg:border-r border-border p-6 lg:p-12 flex items-center">
          <div className="w-full max-w-md mx-auto space-y-6">
            <div className="flex items-center gap-3">
              <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-primary text-primary-foreground">
                <Ship className="h-5 w-5" />
              </div>
              <div>
                <div className="text-xs text-muted-foreground">Crewship</div>
                <h1 className="text-lg font-semibold">Setup</h1>
              </div>
            </div>

            <div className="space-y-0">
              <StepperItem n={1} active={step === 1} done={step > 1} label="Workspace" />
              <Connector />
              <StepperItem n={2} active={step === 2} done={step > 2} label="Crew" />
              <Connector />
              <StepperItem n={3} active={step === 3} done={false} label="Adapter" />
            </div>

            {step === 1 && (
              <div className="space-y-4">
                <div>
                  <h2 className="text-xl font-semibold mb-1">What&apos;s your workspace called?</h2>
                  <p className="text-sm text-muted-foreground">
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
                  />
                </div>
                {runtimeReady === true && (
                  <div className="flex items-center gap-2 text-xs text-emerald-600 dark:text-emerald-400">
                    <Check className="h-3.5 w-3.5" /> Docker detected
                  </div>
                )}
                {runtimeReady === false && (
                  <div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-700 dark:text-amber-400">
                    Docker isn&apos;t reachable. You can still finish setup now and start a container runtime later from Settings.
                  </div>
                )}
              </div>
            )}

            {step === 2 && (
              <div className="space-y-4">
                <div>
                  <h2 className="text-xl font-semibold mb-1">Pick your first crew</h2>
                  <p className="text-sm text-muted-foreground">Watch the preview build itself on the right.</p>
                </div>
                <div className="space-y-2">
                  {CREW_OPTIONS.map((opt) => {
                    const active = crewSlug === opt.slug
                    return (
                      <button
                        key={opt.slug}
                        type="button"
                        aria-pressed={active}
                        onClick={() => setCrewSlug(opt.slug)}
                        className={`flex w-full items-center gap-3 rounded-lg border p-3 text-left transition-colors ${
                          active ? "border-primary bg-primary/5" : "border-border hover:bg-muted"
                        }`}
                      >
                        <span className="text-xl">{opt.emoji}</span>
                        <span className="text-sm font-medium flex-1">{opt.label}</span>
                        <span className="text-xs text-muted-foreground">
                          {opt.slug === "blank" ? "1 agent" : "4 agents"}
                        </span>
                      </button>
                    )
                  })}
                </div>
              </div>
            )}

            {step === 3 && (
              <div className="space-y-4">
                <div>
                  <h2 className="text-xl font-semibold mb-1">How will you work?</h2>
                  <p className="text-sm text-muted-foreground">
                    Browser is the fastest start. Pair a local CLI to drive things from your own machine.
                  </p>
                </div>

                <div className="space-y-2">
                  <ModeRow
                    icon={Globe}
                    title="Chat in browser"
                    description="Quickest start. Paste an API key."
                    active={mode === "browser"}
                    onClick={() => setMode("browser")}
                  />
                  <ModeRow
                    icon={Terminal}
                    title="Pair my local CLI"
                    description="Claude Code, Gemini, Codex, OpenCode, Cursor, Factory Droid."
                    active={mode === "cli"}
                    onClick={() => setMode("cli")}
                  />
                </div>

                {mode === "browser" && (
                  <div className="space-y-3">
                    <div className="space-y-2">
                      <Label>CLI Adapter</Label>
                      <div className="grid grid-cols-2 gap-2">
                        {CLI_ADAPTER_KEYS.map((key) => {
                          const cfg = CLI_ADAPTERS[key]
                          const Icon = cfg.icon
                          const active = adapter === key
                          return (
                            <button
                              key={key}
                              type="button"
                              aria-pressed={active}
                              onClick={() => {
                                setAdapter(key)
                                setModel(cfg.defaultModel)
                              }}
                              className={`flex items-center gap-2 rounded-lg border p-2.5 text-left transition-colors ${
                                active ? "border-primary bg-primary/5" : "border-border hover:bg-muted"
                              }`}
                            >
                              <Icon className={`h-4 w-4 shrink-0 ${active ? "text-primary" : "text-muted-foreground"}`} />
                              <span className="text-xs font-medium truncate">{cfg.label}</span>
                            </button>
                          )
                        })}
                      </div>
                    </div>
                    <div className="space-y-2">
                      <Label htmlFor="model">Model</Label>
                      <Select value={model} onValueChange={setModel}>
                        <SelectTrigger id="model" className="font-mono text-xs">
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
                        className="font-mono text-xs"
                      />
                    </div>
                  </div>
                )}

                {mode === "cli" && (
                  <div className="space-y-3">
                    <div className="text-xs text-muted-foreground">
                      Run this on the machine where your CLI lives. Works with any of the six supported
                      adapters — the server doesn&apos;t care which.
                    </div>
                    {pairCode ? (
                      <>
                        <div className="flex items-center justify-between gap-2 rounded-md border border-border bg-muted/50 p-3">
                          <code className="text-xs font-mono text-emerald-600 dark:text-emerald-400 break-all">
                            $ crewship login --pair --code={pairCode}
                          </code>
                          <Button
                            type="button"
                            variant="ghost"
                            size="sm"
                            onClick={copyPairCmd}
                            className="shrink-0"
                            aria-label="Copy command"
                          >
                            {pairCopied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
                          </Button>
                        </div>
                        {pairStatus === "pending" && (
                          <div className="flex items-center gap-2 text-xs text-amber-600 dark:text-amber-400">
                            <div className="h-1.5 w-1.5 rounded-full bg-amber-500 animate-pulse" />
                            Waiting for your CLI to connect…
                          </div>
                        )}
                        {pairStatus === "consumed" && (
                          <div className="flex items-center gap-2 text-xs text-emerald-600 dark:text-emerald-400">
                            <Check className="h-3.5 w-3.5" /> CLI paired. Ready to launch.
                          </div>
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
                          <div className="text-[10px] text-muted-foreground">
                            Expires at {new Date(pairExpiresAt).toLocaleTimeString()}.
                          </div>
                        )}
                      </>
                    ) : (
                      <div className="flex items-center gap-2 text-xs text-muted-foreground">
                        <Loader2 className="h-3.5 w-3.5 animate-spin" /> Generating code…
                      </div>
                    )}
                  </div>
                )}
              </div>
            )}

            {error && (
              <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
                {error}
              </div>
            )}

            <div className="flex items-center justify-between pt-2">
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
                <Button type="button" variant="ghost" size="sm" onClick={handleSkip} className="text-muted-foreground">
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
            adapterLabel={adapterCfg?.label}
          />
        </div>
      </div>
    </div>
  )
}

function StepperItem({ n, active, done, label }: { n: number; active: boolean; done: boolean; label: string }) {
  return (
    <div className="flex items-center gap-3 text-sm">
      <div
        className={`flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium transition-colors ${
          done
            ? "bg-primary text-primary-foreground"
            : active
              ? "bg-primary/10 text-primary border-2 border-primary"
              : "bg-muted text-muted-foreground"
        }`}
      >
        {done ? <Check className="h-3.5 w-3.5" /> : n}
      </div>
      <span className={active || done ? "font-medium" : "text-muted-foreground"}>{label}</span>
    </div>
  )
}

function Connector() {
  return <div className="ml-3 w-px h-3 bg-border" />
}

function ModeRow({
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
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={`flex w-full items-center gap-3 rounded-lg border p-3 text-left transition-colors ${
        active ? "border-primary bg-primary/5" : "border-border hover:bg-muted"
      }`}
    >
      <Icon className={`h-5 w-5 shrink-0 ${active ? "text-primary" : "text-muted-foreground"}`} />
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{title}</div>
        <div className="text-xs text-muted-foreground">{description}</div>
      </div>
    </button>
  )
}
