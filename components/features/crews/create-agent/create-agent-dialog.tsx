"use client"

import { useCallback, useEffect, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import {
  ArrowRight,
  Brain,
  ChevronDown,
  Cpu,
  Image as ImageIcon,
  Layers,
  Sparkles,
  TriangleAlert,
  Wrench,
} from "lucide-react"
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@/components/ui/popover"
import { Switch } from "@/components/ui/switch"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
  CreateSurfaceDisclosure,
  CreateSurfaceField,
  CreateSurfaceFooter,
  CreateSurfaceGrid,
  CreateSurfaceHeader,
  CreateSurfaceNotice,
  CreateSurfaceRefusal,
  CreateSurfaceSection,
  CreateSurfaceToggleRow,
} from "@/components/layout/create-surface"
import { cn } from "@/lib/utils"
import { CrewPicker } from "@/components/features/crews/crew-picker"
import { apiFetch } from "@/lib/api-fetch"
import { AVATAR_STYLES, DEFAULT_AVATAR_STYLE, getAgentAvatarUrl } from "@/lib/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import { BUILTIN_PERSONAS, type AgentPersona } from "@/lib/entities"
import { AvatarPickerBody } from "@/components/features/crews/avatar-picker-dialog"
import { TemplateBrowser } from "./template-browser"
import {
  AgentAccessSection,
  applyAgentAccess,
  useAgentAccessCatalog,
  type AgentAccessSelection,
} from "./agent-access"
import { MODELS_BY_PROVIDER, defaultModelForProvider, isKnownModel } from "./llm-models"
import {
  applyPersonaDefaults,
  initialAgentDraft,
  isDraftDirty,
  isIdentityValid,
  resolveFinalPrompt,
  type CrewLite,
} from "./types"
import type { LLMProvider } from "@/lib/entities"

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
 *  separate component.
 *
 *  The `max-sm:` half is the shell's touch-target rule applied to the plain
 *  inputs it does not own: `min-h-12` is 44.16px here because this project
 *  sets `--spacing: 0.23rem` — see the header comment on create-surface.tsx.
 *  `h-11` would land at 40.5px and look fine while missing the target. */
const INPUT_CLASS =
  "w-full bg-background border border-white/[0.15] rounded-md px-2.5 py-1.5 text-[13px] text-foreground outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/15 max-sm:min-h-12 max-sm:text-sm"

const TOOL_PROFILES = ["MINIMAL", "CODING", "FULL"] as const
const CLI_ADAPTERS = ["CLAUDE_CODE", "OPENCODE", "CODEX_CLI", "GEMINI_CLI", "CURSOR_CLI", "FACTORY_DROID"] as const
const LLM_PROVIDERS = ["ANTHROPIC", "OPENAI", "GOOGLE", "CURSOR", "FACTORY", "OLLAMA"] as const

/** Single-screen Create Agent dialog. Replaces the 3-step wizard with one
 *  surface that mirrors the field set of POST /api/v1/agents 1:1.
 *
 *  Mounts the shared shell (components/layout/create-surface.tsx) at size
 *  `lg`, so the overlay, the focus trap, Esc, ⌘↵, the discard guard, the
 *  bottom-sheet phone layout and the never-scrolling footer are one
 *  implementation rather than this file's own. What is left here is the
 *  form and the submit.
 *
 *  Layout (top → bottom):
 *    - Template: one row stating the current pick, opening the catalogue
 *    - Identity: avatar (picker) | name | crew | slug | role | role title |
 *      description
 *    - Persona textarea (always visible, pre-filled from chosen template)
 *    - Runtime: model select + memory toggle (90% of users stop here)
 *    - Advanced disclosure: tool_profile + cli_adapter + llm_provider +
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
  // Upgrade lazy-loaded DiceBear styles from placeholder to real avatar.
  useAvatarStylesVersion()
  const router = useRouter()
  const [draft, setDraft] = useState(() => initialAgentDraft(defaultCrewSlug))
  const [submitting, setSubmitting] = useState(false)
  // Ref for the in-flight check inside submit() — using `submitting` state
  // there would close over a stale value and let a fast double-fire through
  // before the next render wires up the disabled button.
  const submittingRef = useRef(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  const [browserOpen, setBrowserOpen] = useState(false)
  // Held beside the draft rather than inside it: these are not fields of
  // POST /api/v1/agents, they are two follow-up calls the draft has no shape
  // for, and the submit-body test asserts the draft maps 1:1 to the Go struct.
  const [access, setAccess] = useState<AgentAccessSelection>({ integrationIds: [], channelIds: [] })
  const accessCatalog = useAgentAccessCatalog(workspaceId, open)
  // What the server said when it said no. Rendered in the shell's refusal
  // band between the body and the footer — the toast alone had already
  // faded by the time anyone looked up from the form.
  const [refusal, setRefusal] = useState<string | null>(null)

  // Same reset-on-open-only pattern as the old wizard: capture latest
  // defaultCrewSlug via ref so a parent prop change while the dialog is
  // open doesn't wipe what the user typed.
  const defaultCrewSlugRef = useRef(defaultCrewSlug)
  useEffect(() => { defaultCrewSlugRef.current = defaultCrewSlug }, [defaultCrewSlug])
  // The crew the draft was SEEDED with, held separately from the prop for
  // the same reason: it is the baseline the discard guard compares against.
  const [baselineCrewSlug, setBaselineCrewSlug] = useState(defaultCrewSlug)
  const wasOpenRef = useRef(false)
  useEffect(() => {
    if (open && !wasOpenRef.current) {
      setDraft(initialAgentDraft(defaultCrewSlugRef.current))
      setBaselineCrewSlug(defaultCrewSlugRef.current)
      setSubmitting(false)
      setBrowserOpen(false)
      setAccess({ integrationIds: [], channelIds: [] })
      setRefusal(null)
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
  const crewName = crews.find((c) => c.slug === draft.crewSlug)?.name

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
    setRefusal(null)
    try {
      const targetCrew = requiresCrew
        ? crews.find((c) => c.slug === draft.crewSlug) ?? null
        : null
      if (requiresCrew && !targetCrew) {
        const message = `Crew "${draft.crewSlug}" no longer exists. Please reselect.`
        toast.error(message)
        setRefusal(message)
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
      const res = await apiFetch(
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

      // Bindings are keyed on an agent that exists, so they are spent here
      // rather than in the body above. Failures are reported, not thrown: the
      // agent is created either way, and an agent quietly missing the tool it
      // was created for is worse than being told where to add it.
      const failed =
        access.integrationIds.length || access.channelIds.length
          ? await applyAgentAccess(workspaceId, created.id, access, accessCatalog)
          : []

      if (failed.length) {
        toast.warning(`Agent "${created.name}" created, but ${failed.length} grant${failed.length === 1 ? "" : "s"} did not apply`, {
          description: `${failed.join(", ")} — add them from the agent's canvas.`,
        })
      } else {
        toast.success(`Agent "${created.name}" created`)
      }
      onOpenChange(false)
      onCreated(created.slug)
      router.replace(`/crews?agent=${encodeURIComponent(created.slug)}`)
    } catch (err) {
      const message = `Could not create agent: ${err instanceof Error ? err.message : String(err)}`
      toast.error(message)
      setRefusal(message)
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }, [draft, crews, requiresCrew, workspaceId, finalPrompt, access, accessCatalog, onOpenChange, onCreated, router])

  // ⌘↵ / Ctrl↵ is wired by the shell — this is only the "is it submittable"
  // guard the shell asks callers to keep inside their own handler.
  const handleShortcutSubmit = useCallback(() => {
    if (!valid || submitting) return
    void submit()
  }, [valid, submitting, submit])

  return (
      <CreateSurface
        open={open}
        onOpenChange={onOpenChange}
        size="lg"
        dirty={isDraftDirty(draft, baselineCrewSlug)}
        discardLabel="this agent"
        onSubmit={() => {
          // ⌘↵ inside the picker closes the picker; it must not also create
          // the agent underneath it. Same rule the crew wizard applies to
          // its base-image and icon panels.
          if (pickerOpen) { setPickerOpen(false); return }
          handleShortcutSubmit()
        }}
      >
        <CreateSurfaceHeader
          concept="crews"
          accent="purple"
          context={crewName}
          title={pickerOpen ? "Avatar — new agent" : "New agent"}
          description={
            pickerOpen
              ? "Pick a style and a seed. The same seed always produces the same face."
              : "Pick a template to start fast, or fill in the basics."
          }
          onBack={pickerOpen ? () => setPickerOpen(false) : undefined}
          onClose={() => onOpenChange(false)}
        />

        <CreateSurfaceBody className="flex flex-col gap-5">
          {/* The avatar picker is a PANEL: the surface swaps its body for it
              and the back arrow returns. It used to be a second Radix dialog
              stacked on this one — two focus traps, two Escape handlers, and
              a discard guard on the outer surface that could not see the
              inner. New crew's icon picker was moved off that pattern for the
              same reasons; this is the last create surface still on it. */}
          {pickerOpen && (
            <AvatarPickerBody
              agentName={draft.name || "agent"}
              seed={draft.avatarSeed}
              style={draft.avatarStyle}
              crewStyle={null}
              onChange={({ seed: nextSeed, style: nextStyle }) =>
                setDraft((d) => ({
                  ...d,
                  avatarSeed: nextSeed,
                  // The draft's avatarStyle is a plain string, so "follow the
                  // crew" (null) resolves to the default here rather than
                  // being stored as an inherit marker — the agent has no crew
                  // row to inherit from until it exists.
                  avatarStyle: nextStyle ?? DEFAULT_AVATAR_STYLE,
                  avatarTouched: true,
                }))
              }
            />
          )}

          {!pickerOpen && hasNoCrews && (
            <CreateSurfaceNotice tone="warn" icon={TriangleAlert}>
              This workspace has <strong className="text-foreground">no crews yet</strong>. Agents (and
              Leads) live inside a crew — create one first, or set this agent as a{" "}
              <strong className="text-foreground">Coordinator</strong> (workspace-wide, no crew
              required).
            </CreateSurfaceNotice>
          )}

          {/* Everything below is the form. Hidden wholesale while the panel
              is up: the panel replaces the body, it does not sit beside it. */}
          {!pickerOpen && (
            <>
          {/* ─── Template ───
              One line, not a wall of pills.
              
              This was six featured PersonaChips (avatar + name + role title
              each) plus "All 30 templates" plus "Blank" — eight pills that
              wrap to two rows at the surface's 800px and take the top of the
              form before a single field is reached. Template is OPTIONAL and
              most people either take the first thing that looks right or skip
              it, so it does not deserve more vertical space than Identity.
              
              What replaces it is the shape a single-choice control normally
              has: a row that states the current answer and opens the full
              catalogue. The six featured are not lost — they were an
              arbitrary slice of the same thirty, and the catalogue behind
              this row has all of them with search and categories. */}
          <CreateSurfaceSection
            title="Template"
            icon={Sparkles}
            accent="gold"
            hint="optional · pre-fills prompt + LLM + avatar"
          >
            <div className="flex items-center gap-2">
              <Popover open={browserOpen} onOpenChange={setBrowserOpen}>
                <PopoverTrigger asChild>
                  <button
                    type="button"
                    aria-label="Choose a template"
                    className="flex min-w-0 flex-1 items-center gap-2.5 rounded-md border border-white/[0.15] bg-background px-2 py-1.5 text-left text-[13px] transition-colors hover:border-white/[0.28] focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/15 max-sm:min-h-12"
                  >
                    {draft.selectedPersona ? (
                      <>
                        <span className="h-6 w-6 shrink-0 overflow-hidden rounded-full border border-white/10 bg-muted">
                          <img
                            src={getAgentAvatarUrl(
                              draft.selectedPersona.suggestedSlug,
                              draft.selectedPersona.avatarStyle,
                            )}
                            alt=""
                            className="h-full w-full"
                          />
                        </span>
                        <span className="min-w-0 flex-1 truncate">
                          <span className="font-medium">{draft.selectedPersona.name}</span>
                          <span className="text-muted-foreground"> · {draft.selectedPersona.roleTitle}</span>
                        </span>
                      </>
                    ) : (
                      <>
                        <Layers className="h-4 w-4 shrink-0 text-muted-foreground" />
                        <span className="min-w-0 flex-1 truncate text-muted-foreground">
                          Blank — browse {BUILTIN_PERSONAS.length} templates
                        </span>
                      </>
                    )}
                    <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
                  </button>
                </PopoverTrigger>
                <PopoverContent
                  align="start"
                  sideOffset={6}
                  className="w-[640px] max-w-[calc(100vw-2rem)] p-0 bg-card border-white/[0.08]"
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

              {/* Only when there is something to clear. As a permanent
                  "Blank" pill it was a second control competing with the
                  picker for the same decision even when nothing was picked. */}
              {draft.selectedPersona !== null && (
                <button
                  type="button"
                  onClick={handleBlank}
                  className="shrink-0 rounded-md px-2 py-1.5 text-[12px] text-muted-foreground transition-colors hover:text-foreground max-sm:min-h-12"
                >
                  Clear
                </button>
              )}
            </div>
          </CreateSurfaceSection>

          {/* ─── Identity ─── */}
          <CreateSurfaceSection title="Identity" icon={ImageIcon} accent="purple">
            {/* `items-end` keeps the 56px tile bottom-aligned with the input
                next to it, the way the old grid did. */}
            <div className="flex items-start gap-3">
              {/* No badge in the corner.
               *
               * A pencil in a filled circle at `-bottom-1 -right-1` works on
               * New crew, where the tile is a flat glyph on a gradient and
               * the corner is empty. It does not work here: a DiceBear
               * portrait fills the whole tile, so the badge always lands on
               * the face — which is why it read as a blob stuck to the
               * robot's chin rather than as a control.
               *
               * The affordance moves to the caption under the name, which is
               * the pattern New crew's identity step already uses for the
               * same job ("Rocket · blue — tap to change"). It says what the
               * avatar currently IS, which the badge never did, it is a
               * thumb-sized target with the `max-sm:` padding, and it leaves
               * the portrait alone. */}
              <button
                type="button"
                onClick={() => setPickerOpen(true)}
                title="Customize avatar"
                aria-label="Customize avatar"
                aria-haspopup="dialog"
                aria-expanded={pickerOpen}
                className="mt-5 h-14 w-14 shrink-0 overflow-hidden rounded-xl border border-white/10 bg-muted outline-none transition-colors hover:border-primary/50 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
                <img src={avatarUrl} alt="" aria-hidden="true" className="h-full w-full object-cover" />
              </button>

              <div className="min-w-0 flex-1">
                <CreateSurfaceField label="Name" htmlFor="agent-name" required>
                  <input
                    id="agent-name"
                    type="text"
                    value={draft.name}
                    onChange={(e) => setDraft({ ...draft, name: e.target.value })}
                    placeholder="Filip"
                    autoFocus
                    className={INPUT_CLASS}
                  />
                </CreateSurfaceField>
                <button
                  type="button"
                  onClick={() => setPickerOpen(true)}
                  className="mt-1 block text-[11px] text-muted-foreground-soft transition-colors hover:text-foreground/80 max-sm:px-2 max-sm:py-4"
                >
                  {AVATAR_STYLES[draft.avatarStyle]?.label ?? draft.avatarStyle} — tap to change
                </button>
              </div>
            </div>

            <CreateSurfaceGrid>
              {/* A picker, not a <select>. The native list was every crew in
                  the workspace as one alphabetical column of names — no icon,
                  no colour, no search — which on a box with a few dozen crews
                  is a wall of near-identical strings. The icon and colour are
                  what the roster, the sidebar and every issue already use to
                  tell crews apart. */}
              {requiresCrew ? (
                <CreateSurfaceField label="Crew" htmlFor="agent-crew" required>
                  <CrewPicker
                    id="agent-crew"
                    by="slug"
                    crews={crews}
                    value={draft.crewSlug}
                    onChange={(crewSlug) => setDraft({ ...draft, crewSlug })}
                    placeholder="Pick crew…"
                    ariaLabel="Crew"
                  />
                </CreateSurfaceField>
              ) : (
                <CreateSurfaceField label="Crew" htmlFor="agent-crew" hint="N/A for Coordinator">
                  <input
                    id="agent-crew"
                    className={cn(INPUT_CLASS, "text-muted-foreground")}
                    value="— workspace-wide —"
                    disabled
                  />
                </CreateSurfaceField>
              )}

              <CreateSurfaceField label="Slug" htmlFor="agent-slug" hint="auto from name">
                <input
                  id="agent-slug"
                  type="text"
                  value={draft.slug}
                  onChange={(e) =>
                    setDraft({ ...draft, slug: e.target.value, slugTouched: true })
                  }
                  placeholder="filip"
                  className={cn(INPUT_CLASS, "font-mono text-[12.5px]")}
                />
              </CreateSurfaceField>

              {/* A chip row, like Tool profile further down this same form.
                  Two short options is the case the kit's own note says a
                  <select> loses: both are visible without opening anything,
                  each hint explains the role rather than stating a limit, and
                  the tap target is the chip instead of a 16px caret. */}
              <CreateSurfaceField label="Role">
                <CreateSurfaceChoice
                  ariaLabel="Agent role"
                  value={draft.agentRole}
                  onChange={(agentRole) => setDraft({ ...draft, agentRole })}
                  options={[
                    { value: "AGENT", label: "Agent", hint: "Works on what it is given" },
                    { value: "LEAD", label: "Lead", hint: "Can plan and delegate to the crew" },
                  ]}
                />
              </CreateSurfaceField>

              <CreateSurfaceField
                label="Role title"
                htmlFor="agent-role-title"
                hint="optional · e.g. 'Senior Backend'"
              >
                <input
                  id="agent-role-title"
                  type="text"
                  value={draft.roleTitle}
                  onChange={(e) => setDraft({ ...draft, roleTitle: e.target.value })}
                  placeholder="Data Analyst"
                  className={INPUT_CLASS}
                />
              </CreateSurfaceField>
            </CreateSurfaceGrid>

            <CreateSurfaceField
              label="Description"
              htmlFor="agent-description"
              hint="optional · shown in roster"
            >
              <input
                id="agent-description"
                type="text"
                value={draft.description}
                onChange={(e) => setDraft({ ...draft, description: e.target.value })}
                placeholder="What does this agent do, in one line?"
                className={INPUT_CLASS}
              />
            </CreateSurfaceField>
          </CreateSurfaceSection>

          {/* ─── Persona ─── */}
          <CreateSurfaceSection
            title="Persona"
            icon={Brain}
            accent="purple"
            hint="how should this agent behave"
          >
            <textarea
              id="agent-persona"
              aria-label="Persona system prompt"
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
              className="w-full min-h-[140px] max-h-[260px] resize-y bg-background border border-white/[0.15] rounded-md px-3 py-2 text-[12px] font-mono leading-relaxed outline-none focus:border-primary focus:ring-2 focus:ring-primary/15"
            />
            <div className="flex items-start gap-2">
              <p className="min-w-0 flex-1 text-[10.5px] text-muted-foreground flex items-center gap-1.5">
                {isPromptFromTemplate && draft.selectedPersona ? (
                  <>
                    <span className="text-[9px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-success/15 text-success border border-success/25">
                      Pre-filled
                    </span>
                    <span>
                      From <strong className="text-foreground/80">{draft.selectedPersona.name}</strong>.
                      Edit freely — saves only on this agent.
                    </span>
                  </>
                ) : draft.editedPersonaPrompt !== null && draft.selectedPersona ? (
                  <>
                    <span className="text-[9px] font-bold uppercase tracking-wider px-1.5 py-0.5 rounded bg-warn/15 text-warn border border-warn/25">
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
              {(draft.editedPersonaPrompt !== null || draft.customPrompt.trim()) && (
                <button
                  type="button"
                  onClick={handleResetPrompt}
                  className="shrink-0 text-[11.5px] text-primary hover:text-primary/80"
                >
                  Reset
                  {draft.selectedPersona ? ` to ${draft.selectedPersona.name}` : ""}
                </button>
              )}
            </div>
          </CreateSurfaceSection>

          {/* ─── Runtime (model + memory only — most common) ─── */}
          <CreateSurfaceSection title="Runtime" icon={Cpu} accent="teal">
            <CreateSurfaceField
              label="Model"
              htmlFor="agent-model"
              hint={`from ${draft.llmProvider.toLowerCase()}`}
            >
              <ModelInput
                id="agent-model"
                provider={draft.llmProvider}
                value={draft.llmModel}
                onChange={(model) => setDraft({ ...draft, llmModel: model })}
              />
            </CreateSurfaceField>

            {/* "on" / "off" restated the switch beside it and said nothing
                about what is being switched. The agent canvas already words
                this properly ("Memory between sessions — without it every
                session starts from nothing", config-tab.tsx); the create form
                was the one place that only had the state. Same sentence, so
                the two surfaces cannot drift into describing it differently. */}
            <CreateSurfaceToggleRow
              concept="memory"
              label="Memory between sessions"
              hint={
                draft.memoryEnabled
                  ? "Notes it keeps and can search later — AGENT.md, a daily journal, lessons."
                  : "Without it every session starts from nothing."
              }
              control={
                <Switch
                  aria-label="Memory"
                  checked={draft.memoryEnabled}
                  onCheckedChange={(next) => setDraft({ ...draft, memoryEnabled: next })}
                />
              }
            />
          </CreateSurfaceSection>

          {/* ─── Tools & notifications ───
              Between Runtime and Advanced on purpose. It is not advanced —
              "which tools may this one call" is a question people have while
              filling the form, and the answer differs from its crew's more
              often than not: a Security Analyst and a Copywriter in the same
              container should not hold the same integrations. The crew's
              Container step decides what is INSTALLED; this decides what this
              agent may CALL and where it may post. */}
          <AgentAccessSection
            catalog={accessCatalog}
            selection={access}
            onChange={setAccess}
          />


          {/* ─── Advanced ───
              The lid carries the CURRENT values, not the field names: the
              point of a disclosure is that you can decide not to open it.
              Wrapped in its own `shrink-0`: the body is a flex column that is
              shorter than its content on any viewport (twenty fields do not
              fit in 92dvh), so every child is a shrinkable flex item — and
              `CreateSurfaceDisclosure`'s own root carries `overflow-hidden`.
              Per the flex sizing spec, a flex item's automatic minimum size
              collapses to 0 the moment its overflow is not `visible`, so this
              was the one section with nothing stopping flex-shrink from
              eating it down to ~2px while its siblings (no overflow-hidden of
              their own, so a real min-content floor) kept their full size —
              measured: the whole "Advanced" row rendered under 2px tall,
              button included, on a phone viewport. `shrink-0` on a plain
              wrapper div moves the flex item one level up, off the
              overflow-hidden element, so IT keeps its content-based min size
              and the disclosure inside renders at its real height again. */}
          <div className="shrink-0">
          <CreateSurfaceDisclosure
            icon={Wrench}
            accent="amber"
            label="Advanced"
            summary={`${draft.toolProfile.toLowerCase()} tools · ${draft.cliAdapter
              .toLowerCase()
              .replace(/_/g, " ")} · ${draft.llmProvider.toLowerCase()} · ${Math.round(
              draft.timeoutSeconds / 60,
            )} min${draft.agentRole === "LEAD" ? ` · ${draft.leadMode}` : ""}`}
          >
            <CreateSurfaceField label="Tool profile" hint="what tools the agent can call">
              <CreateSurfaceChoice
                ariaLabel="Tool profile"
                value={draft.toolProfile}
                options={TOOL_PROFILES.map((v) => ({ value: v, label: v }))}
                onChange={(v) => setDraft({ ...draft, toolProfile: v })}
              />
            </CreateSurfaceField>

            <CreateSurfaceField label="CLI adapter" hint="which CLI runs in the container">
              <CreateSurfaceChoice
                ariaLabel="CLI adapter"
                value={draft.cliAdapter}
                options={CLI_ADAPTERS.map((v) => ({ value: v, label: v }))}
                onChange={(v) => setDraft({ ...draft, cliAdapter: v })}
              />
            </CreateSurfaceField>

            <CreateSurfaceField label="LLM provider" hint="changing this swaps the model list">
              <CreateSurfaceChoice
                ariaLabel="LLM provider"
                value={draft.llmProvider}
                options={LLM_PROVIDERS.map((v) => ({ value: v, label: v }))}
                onChange={(v) => {
                  // Auto-reset model to the provider's default when
                  // the user toggles. The previous model string is
                  // (almost certainly) wrong for the new provider —
                  // claude-opus on OPENAI would be a runtime error
                  // hours later.
                  const newProvider: LLMProvider = v
                  const keepModel = isKnownModel(newProvider, draft.llmModel)
                  setDraft({
                    ...draft,
                    llmProvider: newProvider,
                    llmModel: keepModel ? draft.llmModel : defaultModelForProvider(newProvider),
                  })
                }}
              />
            </CreateSurfaceField>

            <CreateSurfaceGrid>
              <CreateSurfaceField label="Timeout" htmlFor="agent-timeout" hint="seconds">
                <input
                  id="agent-timeout"
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
              </CreateSurfaceField>
              {draft.agentRole === "LEAD" && (
                <CreateSurfaceField label="Lead mode" htmlFor="agent-lead-mode">
                  <select
                    id="agent-lead-mode"
                    value={draft.leadMode}
                    onChange={(e) =>
                      setDraft({ ...draft, leadMode: e.target.value as "active" | "passive" })
                    }
                    className={INPUT_CLASS}
                  >
                    <option value="active">active</option>
                    <option value="passive">passive</option>
                  </select>
                </CreateSurfaceField>
              )}
            </CreateSurfaceGrid>

            <p className="text-[10.5px] text-muted-foreground">
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
          </CreateSurfaceDisclosure>
          </div>
            </>
          )}
        </CreateSurfaceBody>

        <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

        <CreateSurfaceFooter
          hint={validationHint ? <span className="text-warn">{validationHint}</span> : undefined}
          onCancel={() => onOpenChange(false)}
          primaryLabel={submitting ? "Creating…" : "Create agent"}
          primaryIcon={ArrowRight}
          primaryDisabled={!valid}
          busy={submitting}
          onPrimary={() => void submit()}
        />
      </CreateSurface>
  )
}

/** Model picker that adapts to the current provider:
 *    - dropdown listing the curated models for that provider
 *    - "(custom…)" option flips the input into a free-text field, useful for
 *      Ollama where model names are whatever the user has pulled locally,
 *      and for early-access provider models not yet in our list. */
function ModelInput({
  id,
  provider,
  value,
  onChange,
}: {
  id: string
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
          id={id}
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
      id={id}
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
