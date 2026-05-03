"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import {
  ArrowRight,
  ChevronRight,
  Layers,
  Loader2,
  Pencil,
  Search,
  X,
} from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { cn } from "@/lib/utils"
import { getAgentAvatarUrl } from "@/lib/agent-avatar"
import { BUILTIN_PERSONAS, type AgentPersona } from "@/lib/agent-personas"
import { AvatarPickerDialog } from "@/components/features/crews/avatar-picker-dialog"
import { TemplateBrowser } from "./template-browser"
import { PersonaChip, BlankChip } from "./persona-chip"
import { MODELS_BY_PROVIDER, defaultModelForProvider, isKnownModel } from "./llm-models"
import {
  applyPersonaDefaults,
  initialAgentDraft,
  isIdentityValid,
  resolveFinalPrompt,
  type AgentDraft,
  type CrewLite,
} from "./types"
import type { LLMProvider } from "@/lib/agent-personas"

export interface CreateAgentDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  defaultCrewSlug: string | null
  crews: CrewLite[]
  onCreated: (slug: string) => void
}

/** Shared input/select styling. Centralised so the form looks consistent
 *  without falling back to a global stylesheet hack. Mirrors what other
 *  Crews dialogs use; small enough to inline rather than carve out a
 *  separate component. */
const INPUT_CLASS =
  "w-full bg-zinc-950 border border-white/[0.15] rounded-md px-2.5 py-1.5 text-[13px] text-foreground outline-none transition-colors focus:border-blue-400 focus:ring-2 focus:ring-blue-400/15"

const TOOL_PROFILES = ["MINIMAL", "CODING", "MESSAGING", "FULL"] as const
const CLI_ADAPTERS = ["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI"] as const
const LLM_PROVIDERS = ["ANTHROPIC", "OPENAI", "GOOGLE", "OLLAMA"] as const

/** Slim selection of personas shown as chips at the top. The full list lives
 *  behind the "All templates" popover. Built-ins were ordered to mix Lead +
 *  Agent roles + the most distinct categories so the row is visually varied. */
const FEATURED_IDS = ["b_filip", "b_tomas", "b_viktor", "b_eva", "b_lucie", "b_radek"]

/** Single-screen Create Agent dialog. Replaces the 3-step wizard with one
 *  surface that mirrors the field set of POST /api/v1/agents 1:1.
 *
 *  Layout (top → bottom):
 *    - Template chips (6 featured + "All templates" popover + "Blank")
 *    - Identity row: avatar (picker) | name | crew | slug | role | description
 *    - Persona textarea (always visible, pre-filled from chosen template)
 *    - Runtime row: model select + memory toggle (90% of users stop here)
 *    - Advanced collapsible: tool_profile + cli_adapter + llm_provider +
 *      timeout + lead_mode (visible only for LEAD role)
 *
 *  Submit body matches the fields in internal/api/agents_create.go's
 *  createAgentRequest struct — there's a unit test guarding the shape. */
export function CreateAgentDialog({
  workspaceId,
  open,
  onOpenChange,
  defaultCrewSlug,
  crews,
  onCreated,
}: CreateAgentDialogProps) {
  const router = useRouter()
  const [draft, setDraft] = useState(() => initialAgentDraft(defaultCrewSlug))
  const [submitting, setSubmitting] = useState(false)
  // Ref for the in-flight check inside submit() — using `submitting` state
  // there would close over a stale value and let a fast double-fire through
  // before the next render wires up the disabled button.
  const submittingRef = useRef(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [browserOpen, setBrowserOpen] = useState(false)

  // Same reset-on-open-only pattern as the old wizard: capture latest
  // defaultCrewSlug via ref so a parent prop change while the dialog is
  // open doesn't wipe what the user typed.
  const defaultCrewSlugRef = useRef(defaultCrewSlug)
  useEffect(() => { defaultCrewSlugRef.current = defaultCrewSlug }, [defaultCrewSlug])
  const wasOpenRef = useRef(false)
  useEffect(() => {
    if (open && !wasOpenRef.current) {
      setDraft(initialAgentDraft(defaultCrewSlugRef.current))
      setSubmitting(false)
      setAdvancedOpen(false)
      setBrowserOpen(false)
    }
    wasOpenRef.current = open
  }, [open])

  // Auto-derive slug from name unless user has manually edited it.
  useEffect(() => {
    if (draft.slugTouched) return
    const derived = draft.name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
    if (derived !== draft.slug) setDraft((d) => ({ ...d, slug: derived }))
  }, [draft.name, draft.slug, draft.slugTouched])

  const featured = useMemo(
    () =>
      FEATURED_IDS.map((id) => BUILTIN_PERSONAS.find((p) => p.id === id)).filter(
        (p): p is AgentPersona => !!p,
      ),
    [],
  )

  const seed = draft.avatarSeed || draft.slug || draft.name || "agent"
  const avatarUrl = getAgentAvatarUrl(seed, draft.avatarStyle)
  const requiresCrew = true
  const finalPrompt = resolveFinalPrompt(draft)
  const isPromptFromTemplate =
    draft.selectedPersona !== null &&
    draft.editedPersonaPrompt === null &&
    !draft.customPrompt.trim()
  const valid = isIdentityValid(draft)
  // What's blocking submit? Shown to the user as an inline hint so they
  // don't have to guess why Create is disabled. Mirrors isIdentityValid
  // — keep the order matching so the hint reflects the first failing rule.
  const validationHint: string | null = (() => {
    if (valid) return null
    const trimmedName = draft.name.trim()
    if (trimmedName.length < 2) return "Name must be at least 2 characters"
    if (trimmedName.length > 100) return "Name is too long (max 100 characters)"
    if (draft.slug.length > 50) return "Slug is too long (max 50 characters)"
    if (!/^[a-z0-9-]{2,}$/.test(draft.slug))
      return "Slug must use only lowercase letters, digits, and hyphens (2+ chars)"
    if (requiresCrew && !draft.crewSlug)
      return crews.length === 0 ? "Create a crew first — Coordinator role works without one" : "Pick a crew"
    return null
  })()
  const hasNoCrews = crews.length === 0

  const handlePickPersona = useCallback((persona: AgentPersona) => {
    setDraft((d) => applyPersonaDefaults(d, persona))
    setBrowserOpen(false)
  }, [])

  const handleBlank = useCallback(() => {
    setDraft((d) => ({
      ...d,
      selectedPersona: null,
      editedPersonaPrompt: null,
      customPrompt: "",
    }))
  }, [])

  const handlePromptChange = useCallback((next: string) => {
    setDraft((d) => {
      // Editing the prompt textarea: when a persona is selected, store the
      // edit on editedPersonaPrompt; otherwise treat it as customPrompt.
      if (d.selectedPersona) {
        return { ...d, editedPersonaPrompt: next }
      }
      return { ...d, customPrompt: next }
    })
  }, [])

  const handleResetPrompt = useCallback(() => {
    setDraft((d) => ({ ...d, editedPersonaPrompt: null, customPrompt: "" }))
  }, [])

  const submit = useCallback(async () => {
    if (submittingRef.current) return
    submittingRef.current = true
    setSubmitting(true)
    try {
      const targetCrew = requiresCrew
        ? crews.find((c) => c.slug === draft.crewSlug) ?? null
        : null
      if (requiresCrew && !targetCrew) {
        toast.error(`Crew "${draft.crewSlug}" no longer exists. Please reselect.`)
        submittingRef.current = false
        setSubmitting(false)
        return
      }

      const body = {
        name: draft.name.trim(),
        slug: draft.slug.trim(),
        agent_role: draft.agentRole,
        crew_id: targetCrew?.id ?? null,
        description: draft.description.trim() || null,
        role_title: draft.roleTitle.trim() || null,
        lead_mode: draft.agentRole === "LEAD" ? draft.leadMode : null,
        cli_adapter: draft.cliAdapter,
        llm_provider: draft.llmProvider,
        llm_model: draft.llmModel,
        system_prompt: finalPrompt || null,
        avatar_seed: draft.avatarSeed.trim() || null,
        avatar_style: draft.avatarStyle,
        timeout_seconds: draft.timeoutSeconds,
        tool_profile: draft.toolProfile,
        memory_enabled: draft.memoryEnabled,
      }
      const res = await fetch(
        `/api/v1/agents?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(body),
        },
      )
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `HTTP ${res.status}`)
      }
      const created = await res.json()
      toast.success(`Agent "${created.name}" created`)
      onOpenChange(false)
      onCreated(created.slug)
      router.replace(`/crews?agent=${encodeURIComponent(created.slug)}`)
    } catch (err) {
      toast.error(
        `Could not create agent: ${err instanceof Error ? err.message : String(err)}`,
      )
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }, [draft, crews, requiresCrew, workspaceId, finalPrompt, onOpenChange, onCreated, router])

  // Cmd/Ctrl+Enter submits when valid — mirrors the orchestration / issue
  // dialogs everywhere else in the app.
  useEffect(() => {
    if (!open) return
    const handler = (e: KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key !== "Enter") return
      if (!valid || submitting) return
      e.preventDefault()
      void submit()
    }
    window.addEventListener("keydown", handler)
    return () => window.removeEventListener("keydown", handler)
  }, [open, valid, submitting, submit])

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="p-0 bg-card border-white/[0.08] gap-0 overflow-hidden sm:max-w-[640px]"
        showCloseButton={false}
      >
        {/* Header */}
        <div className="px-5 pt-4 pb-3 border-b border-white/[0.08] flex items-start gap-3">
          <div className="flex-1 min-w-0">
            <DialogTitle asChild>
              <h2 className="text-[15px] font-semibold m-0">New agent</h2>
            </DialogTitle>
            <p className="text-[12px] text-muted-foreground mt-0.5">
              Pick a template to start fast, or fill in the basics.
            </p>
          </div>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            className="text-muted-foreground/70 hover:text-foreground p-0.5"
            aria-label="Close"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <div className="overflow-y-auto max-h-[calc(100vh-180px)]">
          <div className="px-5 py-4 space-y-4">
            {hasNoCrews && (
              <div className="flex gap-2.5 items-start px-3 py-2.5 rounded-lg bg-amber-400/[0.08] border border-amber-400/[0.25] text-[12px]">
                <span className="shrink-0 inline-flex items-center px-1.5 py-0.5 rounded text-[9.5px] font-bold uppercase tracking-wider bg-amber-400/20 text-amber-300 border border-amber-400/30">
                  Heads up
                </span>
                <div className="text-foreground/85 leading-relaxed">
                  This workspace has <strong>no crews yet</strong>. Agents (and Leads) live inside a
                  crew — create one first, or set this agent as a{" "}
                  <strong>Coordinator</strong> (workspace-wide, no crew required).
                </div>
              </div>
            )}

            {/* ─── Templates row ─── */}
            <Section
              label="Template"
              hint="optional · pre-fills prompt + LLM + avatar"
              right={
                <Popover open={browserOpen} onOpenChange={setBrowserOpen}>
                  <PopoverTrigger asChild>
                    <button
                      type="button"
                      className="text-[11.5px] text-blue-400 hover:text-blue-300 inline-flex items-center gap-1"
                    >
                      <Layers className="h-3 w-3" />
                      All {BUILTIN_PERSONAS.length} templates
                    </button>
                  </PopoverTrigger>
                  <PopoverContent
                    align="end"
                    sideOffset={6}
                    className="w-[640px] p-0 bg-card border-white/[0.08]"
                  >
                    <div className="p-3 border-b border-white/[0.08]">
                      <div className="text-[13px] font-semibold mb-0.5">All templates</div>
                      <div className="text-[11.5px] text-muted-foreground">
                        Pick one — we&apos;ll close this and pre-fill everything below.
                      </div>
                    </div>
                    <TemplateBrowser
                      selected={draft.selectedPersona}
                      onSelect={handlePickPersona}
                    />
                  </PopoverContent>
                </Popover>
              }
            >
              <div className="flex gap-1.5 flex-wrap">
                {featured.map((p) => (
                  <PersonaChip
                    key={p.id}
                    persona={p}
                    active={draft.selectedPersona?.id === p.id}
                    onClick={() => handlePickPersona(p)}
                  />
                ))}
                <BlankChip
                  active={draft.selectedPersona === null && !draft.customPrompt}
                  onClick={handleBlank}
                />
              </div>
            </Section>

            {/* ─── Identity row ─── */}
            <Section label="Identity">
              <div className="grid grid-cols-[auto_1fr_1fr] gap-2.5 items-end">
                {/* Avatar tile */}
                <button
                  type="button"
                  onClick={() => setPickerOpen(true)}
                  title="Customize avatar"
                  aria-label="Customize avatar"
                  aria-haspopup="dialog"
                  aria-expanded={pickerOpen}
                  className="group relative w-14 h-14 rounded-xl overflow-hidden border border-white/10 bg-zinc-900 hover:border-blue-400/50 transition-colors"
                >
                  <img src={avatarUrl} alt="" aria-hidden="true" className="w-full h-full object-cover" />
                  <span className="absolute -bottom-1 -right-1 w-5 h-5 bg-blue-500 rounded-full grid place-items-center text-white shadow-md ring-2 ring-card">
                    <Pencil className="h-2.5 w-2.5" />
                  </span>
                </button>

                <FieldShell label="Name" required>
                  <input
                    type="text"
                    value={draft.name}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                    placeholder="Filip"
                    autoFocus
                    className={INPUT_CLASS}
                  />
                </FieldShell>

                {requiresCrew ? (
                  <FieldShell label="Crew" required>
                    <select
                      value={draft.crewSlug}
                      onChange={(e) => setDraft({ ...draft, crewSlug: e.target.value })}
                      className={INPUT_CLASS}
                    >
                      <option value="" disabled>
                        Pick crew…
                      </option>
                      {crews.map((c) => (
                        <option key={c.id} value={c.slug}>
                          {c.name}
                        </option>
                      ))}
                    </select>
                  </FieldShell>
                ) : (
                  <FieldShell label="Crew" hint="N/A for Coordinator">
                    <input
                      className={cn(INPUT_CLASS, "text-muted-foreground")}
                      value="— workspace-wide —"
                      disabled
                    />
                  </FieldShell>
                )}
              </div>

              <div className="grid grid-cols-2 gap-2.5 mt-2">
                <FieldShell label="Slug" hint="auto from name">
                  <input
                    type="text"
                    value={draft.slug}
                    onChange={(e) =>
                      setDraft({ ...draft, slug: e.target.value, slugTouched: true })
                    }
                    placeholder="filip"
                    className={cn(INPUT_CLASS, "font-mono text-[12.5px]")}
                  />
                </FieldShell>
                <FieldShell label="Role">
                  <select
                    value={draft.agentRole}
                    onChange={(e) => {
                      setDraft({ ...draft, agentRole: e.target.value as typeof draft.agentRole })
                    }}
                    className={INPUT_CLASS}
                  >
                    <option value="AGENT">Agent</option>
                    <option value="LEAD">Lead (1 per crew)</option>
                  </select>
                </FieldShell>
              </div>

              <div className="mt-2 grid grid-cols-1 gap-2.5">
                <FieldShell label="Role title" hint="optional · e.g. 'Senior Backend'">
                  <input
                    type="text"
                    value={draft.roleTitle}
                    onChange={(e) => setDraft({ ...draft, roleTitle: e.target.value })}
                    placeholder="Data Analyst"
                    className={INPUT_CLASS}
                  />
                </FieldShell>
                <FieldShell label="Description" hint="optional · shown in roster">
                  <input
                    type="text"
                    value={draft.description}
                    onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                    placeholder="What does this agent do, in one line?"
                    className={INPUT_CLASS}
                  />
                </FieldShell>
              </div>
            </Section>

            {/* ─── Persona ─── */}
            <Section
              label="Persona"
              hint="how should this agent behave"
              right={
                (draft.editedPersonaPrompt !== null || draft.customPrompt.trim()) && (
                  <button
                    type="button"
                    onClick={handleResetPrompt}
                    className="text-[11.5px] text-blue-400 hover:text-blue-300"
                  >
                    Reset
                    {draft.selectedPersona ? ` to ${draft.selectedPersona.name}` : ""}
                  </button>
                )
              }
            >
              <textarea
                value={
                  draft.editedPersonaPrompt !== null
                    ? draft.editedPersonaPrompt
                    : draft.customPrompt ||
                      (draft.selectedPersona ? draft.selectedPersona.systemPrompt : "")
                }
                onChange={(e) => handlePromptChange(e.target.value)}
                placeholder={`You are [name], a [role] in the [crew] crew.

PERSONALITY: …
RESPONSIBILITIES: …
WORK STYLE: …`}
                spellCheck={false}
                className="w-full min-h-[140px] max-h-[260px] resize-y bg-zinc-950 border border-white/[0.15] rounded-md px-3 py-2 text-[12px] font-mono leading-relaxed outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-400/15"
              />
              <p className="text-[10.5px] text-muted-foreground/70 mt-1.5 flex items-center gap-1.5">
                {isPromptFromTemplate && draft.selectedPersona ? (
                  <>
                    <span className="text-[9px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-emerald-400/15 text-emerald-300 border border-emerald-400/25">
                      Pre-filled
                    </span>
                    <span>
                      From <strong className="text-foreground/80">{draft.selectedPersona.name}</strong>.
                      Edit freely — saves only on this agent.
                    </span>
                  </>
                ) : draft.editedPersonaPrompt !== null && draft.selectedPersona ? (
                  <>
                    <span className="text-[9px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-amber-400/15 text-amber-300 border border-amber-400/25">
                      Edited
                    </span>
                    <span>
                      Modified copy of <strong className="text-foreground/80">{draft.selectedPersona.name}</strong>&apos;s prompt.
                    </span>
                  </>
                ) : draft.customPrompt.trim() ? (
                  <span>Custom prompt — used as the agent&apos;s system prompt.</span>
                ) : (
                  <span>Optional. Empty means a generic helpful-assistant prompt.</span>
                )}
              </p>
            </Section>

            {/* ─── Runtime (model + memory only — most common) ─── */}
            <Section label="Runtime">
              <div className="grid grid-cols-[1fr_auto] gap-3 items-end">
                <FieldShell label="Model" hint={`from ${draft.llmProvider.toLowerCase()}`}>
                  <ModelInput
                    provider={draft.llmProvider}
                    value={draft.llmModel}
                    onChange={(model) => setDraft({ ...draft, llmModel: model })}
                  />
                </FieldShell>
                <button
                  type="button"
                  onClick={() => setDraft({ ...draft, memoryEnabled: !draft.memoryEnabled })}
                  className="flex items-center gap-2 pb-2 text-[12px]"
                >
                  <span
                    className={cn(
                      "relative w-[30px] h-[18px] rounded-full transition-colors shrink-0 border",
                      draft.memoryEnabled
                        ? "bg-blue-500 border-transparent"
                        : "bg-white/[0.04] border-white/[0.08]",
                    )}
                  >
                    <span
                      className={cn(
                        "absolute top-0.5 w-3 h-3 rounded-full transition-all",
                        draft.memoryEnabled
                          ? "left-3.5 bg-white"
                          : "left-0.5 bg-muted-foreground",
                      )}
                    />
                  </span>
                  <span>
                    <strong>Memory</strong>{" "}
                    <span className="text-muted-foreground">{draft.memoryEnabled ? "on" : "off"}</span>
                  </span>
                </button>
              </div>
            </Section>

            {/* ─── Advanced collapsible ─── */}
            <div className="border border-white/[0.08] rounded-lg overflow-hidden bg-white/[0.01]">
              <button
                type="button"
                onClick={() => setAdvancedOpen(!advancedOpen)}
                className="w-full px-3.5 py-2.5 flex items-center gap-2 text-[12px] hover:bg-white/[0.02] text-left"
              >
                <ChevronRight
                  className={cn("h-3.5 w-3.5 transition-transform", advancedOpen && "rotate-90")}
                />
                <strong>Advanced</strong>
                <span className="text-muted-foreground/70 text-[11px]">
                  tool profile · CLI adapter · LLM provider · timeout
                  {draft.agentRole === "LEAD" && " · lead mode"}
                </span>
              </button>
              {advancedOpen && (
                <div className="px-3.5 pb-3.5 pt-2 border-t border-white/[0.06] space-y-3">
                  <FieldShell
                    label="Tool profile"
                    hint="what tools the agent can call"
                  >
                    <ChipRow
                      values={TOOL_PROFILES}
                      active={draft.toolProfile}
                      onChange={(v) => setDraft({ ...draft, toolProfile: v })}
                    />
                  </FieldShell>

                  <FieldShell label="CLI adapter" hint="which CLI runs in the container">
                    <ChipRow
                      values={CLI_ADAPTERS}
                      active={draft.cliAdapter}
                      onChange={(v) => setDraft({ ...draft, cliAdapter: v })}
                    />
                  </FieldShell>

                  <FieldShell label="LLM provider" hint="changing this swaps the model list">
                    <ChipRow
                      values={LLM_PROVIDERS}
                      active={draft.llmProvider}
                      onChange={(v) => {
                        // Auto-reset model to the provider's default when
                        // the user toggles. The previous model string is
                        // (almost certainly) wrong for the new provider —
                        // claude-opus on OPENAI would be a runtime error
                        // hours later.
                        const newProvider = v as LLMProvider
                        const keepModel = isKnownModel(newProvider, draft.llmModel)
                        setDraft({
                          ...draft,
                          llmProvider: newProvider,
                          llmModel: keepModel ? draft.llmModel : defaultModelForProvider(newProvider),
                        })
                      }}
                    />
                  </FieldShell>

                  <div className="grid grid-cols-2 gap-2.5">
                    <FieldShell label="Timeout" hint="seconds">
                      <input
                        type="number"
                        step="60"
                        min="60"
                        max="7200"
                        value={draft.timeoutSeconds}
                        onChange={(e) => {
                          // Guard against NaN ('' / non-numeric) and clamp to a
                          // sane range. Without this, an empty field would set
                          // timeout=NaN which the API would reject as 400 with
                          // a confusing 'invalid integer' message.
                          const raw = Number(e.target.value)
                          const safe = Number.isFinite(raw) ? Math.min(7200, Math.max(60, raw)) : 1800
                          setDraft({ ...draft, timeoutSeconds: safe })
                        }}
                        className={cn(INPUT_CLASS, "font-mono")}
                      />
                    </FieldShell>
                    {draft.agentRole === "LEAD" && (
                      <FieldShell label="Lead mode">
                        <select
                          value={draft.leadMode}
                          onChange={(e) =>
                            setDraft({ ...draft, leadMode: e.target.value as "active" | "passive" })
                          }
                          className={INPUT_CLASS}
                        >
                          <option value="active">active</option>
                          <option value="passive">passive</option>
                        </select>
                      </FieldShell>
                    )}
                  </div>

                  <p className="text-[10.5px] text-muted-foreground/60">
                    Not editable here:{" "}
                    <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-white/[0.04]">
                      temperature
                    </code>
                    ,{" "}
                    <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-white/[0.04]">
                      max_tokens
                    </code>
                    ,{" "}
                    <code className="font-mono text-[10px] px-1 py-0.5 rounded bg-white/[0.04]">
                      delegation caps
                    </code>{" "}
                    — set on the agent canvas after create.
                  </p>
                </div>
              )}
            </div>
          </div>
        </div>

        {/* Footer */}
        <div className="px-5 py-3 border-t border-white/[0.08] flex items-center gap-2 bg-card/50">
          <span
            className={cn(
              "text-[11px] mr-auto",
              validationHint ? "text-amber-400" : "text-muted-foreground",
            )}
          >
            {validationHint ?? "⌘↵ to create · Esc to close"}
          </span>
          <button
            type="button"
            onClick={() => onOpenChange(false)}
            disabled={submitting}
            className="text-[12.5px] px-3 py-1.5 rounded-md text-muted-foreground hover:text-foreground hover:bg-white/[0.03] disabled:opacity-40"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void submit()}
            disabled={!valid || submitting}
            className="text-[12.5px] px-3.5 py-1.5 rounded-md bg-blue-500 hover:bg-blue-400 text-white font-medium disabled:opacity-50 flex items-center gap-1.5"
          >
            {submitting ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <ArrowRight className="h-3.5 w-3.5" />
            )}
            {submitting ? "Creating…" : "Create agent"}
          </button>
        </div>

      </DialogContent>

      <AvatarPickerDialog
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        agentName={draft.name || "agent"}
        seed={draft.avatarSeed || null}
        style={draft.avatarStyle}
        crewStyle={null}
        onSave={({ avatar_seed, avatar_style }) => {
          setDraft({
            ...draft,
            avatarSeed: avatar_seed,
            avatarStyle: avatar_style ?? "bottts-neutral",
            avatarTouched: true,
          })
        }}
      />
    </Dialog>
  )
}

function Section({
  label,
  hint,
  right,
  children,
}: {
  label: string
  hint?: string
  right?: React.ReactNode
  children: React.ReactNode
}) {
  return (
    <div>
      <div className="flex items-baseline justify-between gap-2 mb-2">
        <div className="flex items-baseline gap-2 min-w-0">
          <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
            {label}
          </span>
          {hint && (
            <span className="text-[11px] text-muted-foreground/60 truncate">{hint}</span>
          )}
        </div>
        {right && <div className="shrink-0">{right}</div>}
      </div>
      {children}
    </div>
  )
}

function FieldShell({
  label,
  required,
  hint,
  children,
}: {
  label: string
  required?: boolean
  hint?: string
  children: React.ReactNode
}) {
  return (
    <label className="block">
      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground mb-1 flex items-center gap-1">
        <span>{label}</span>
        {required && <span className="text-red-400">*</span>}
        {hint && (
          <span className="normal-case font-normal tracking-normal text-[11px] text-muted-foreground/60">
            — {hint}
          </span>
        )}
      </div>
      {children}
    </label>
  )
}

/** Model picker that adapts to the current provider:
 *    - dropdown listing the curated models for that provider
 *    - "(custom…)" option flips the input into a free-text field, useful for
 *      Ollama where model names are whatever the user has pulled locally,
 *      and for early-access provider models not yet in our list. */
function ModelInput({
  provider,
  value,
  onChange,
}: {
  provider: LLMProvider
  value: string
  onChange: (model: string) => void
}) {
  const known = MODELS_BY_PROVIDER[provider]
  const isCustom = !known.includes(value)

  if (isCustom) {
    return (
      <div className="flex gap-1.5 items-stretch">
        <input
          type="text"
          value={value}
          onChange={(e) => onChange(e.target.value)}
          placeholder="model-name-tag"
          className={cn(INPUT_CLASS, "font-mono text-[12px] flex-1")}
          spellCheck={false}
        />
        <button
          type="button"
          onClick={() => onChange(defaultModelForProvider(provider))}
          title="Switch back to the curated list"
          className="px-2.5 py-1.5 rounded-md text-[11.5px] border border-white/[0.15] hover:bg-white/[0.03] text-foreground/80 whitespace-nowrap"
        >
          ← list
        </button>
      </div>
    )
  }
  return (
    <select
      value={value}
      onChange={(e) => {
        if (e.target.value === "__custom__") {
          // Empty seed so the user knows it's their turn to type.
          onChange("")
          return
        }
        onChange(e.target.value)
      }}
      className={INPUT_CLASS}
    >
      {known.map((m) => (
        <option key={m} value={m}>
          {m}
        </option>
      ))}
      <option value="__custom__" className="italic">
        — custom…
      </option>
    </select>
  )
}

function ChipRow<T extends string>({
  values,
  active,
  onChange,
}: {
  values: readonly T[]
  active: T
  onChange: (v: T) => void
}) {
  return (
    <div className="flex gap-1.5 flex-wrap">
      {values.map((v) => (
        <button
          key={v}
          type="button"
          onClick={() => onChange(v)}
          className={cn(
            "px-2.5 py-1 rounded-md text-[11.5px] font-mono border transition-colors",
            active === v
              ? "bg-blue-500/15 border-blue-400/45 text-blue-300"
              : "bg-card-2 border-white/[0.08] text-foreground/80 hover:border-white/[0.15]",
          )}
        >
          {v}
        </button>
      ))}
    </div>
  )
}
