"use client"

import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "@/lib/toast"
import {
  ArrowRight,
  Brain,
  Cpu,
  Bot,
  CreditCard,
  FileText,
  ShieldCheck,
  MessageSquare,
  Sparkles,
  TriangleAlert,
  Wrench,
} from "lucide-react"
import { Switch } from "@/components/ui/switch"
import { Button } from "@/components/ui/button"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceChoice,
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
import { ApiError, toApiError } from "@/lib/api-error"
import { storeAgentAvatar } from "@/lib/agent-avatar-persist"
import { AVATAR_STYLES, DEFAULT_AVATAR_STYLE } from "@/lib/agent-avatar"
import { AgentAvatar } from "@/components/ui/agent-avatar"
import { useAvatarStylesVersion } from "@/hooks/use-avatar-styles"
import type { AgentPersona } from "@/lib/entities"
import { AvatarPickerBody } from "@/components/features/crews/avatar-picker-dialog"
import {
  AgentAccessSection,
  applyAgentAccess,
  useAgentAccessCatalog,
  type AgentAccessSelection,
} from "./agent-access"
import {
  applyPersonaDefaults,
  initialAgentDraft,
  isDraftDirty,
  isIdentityValid,
  resolveFinalPrompt,
  type CrewLite,
} from "./types"
import { AskFormsBuilder } from "../ask-forms-builder"
import { EditorLayout, EditorPanel } from "../editor-layout"
import { AgentModelSettings } from "./agent-model-settings"
import { RestrictedExecutionField, saveRestrictedProfile, type RestrictedProfile } from "../agent-canvas-tabs/restricted-execution-profile"
import { PaysWithRow, applyPaysWith } from "../agent-canvas-tabs/pays-with-row"
import { saveLearning, type LearningDraft } from "@/components/features/agents/agent-learning-toggle"
import type { LoginCredential } from "@/lib/credentials/provider-logins"
import { PersonaDraft } from "../persona-draft"
import { ConfigTab, SuggestedPromptsField } from "../agent-canvas-tabs/config-tab"
import type { AgentRecord } from "../agent-canvas-tabs/types"
import type { AgentDraft } from "./types"
import { usePageSave } from "@/components/ui/page-save-bar"
import { WizardSteps } from "@/components/ui/wizard-steps"
import { getModelLabel } from "@/lib/cli-adapters"
import { adapterLabel } from "@/lib/credentials/provider-logins"
import { StartFrom, type AgentStart } from "./start-from"
import { SkillChips, assignSkills, type WorkspaceSkillLite } from "./skill-chips"

export interface CreateAgentDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  defaultCrewSlug: string | null
  crews: CrewLite[]
  onCreated: (slug: string) => void
  agent?: AgentRecord
  /**
   * Edit on the agent's page instead of in a dialog: only these sections
   * render, without the surface's header and footer, and the edits register
   * with the page's one save bar (PageSaveProvider).
   */
  inlineSections?: AgentEditorSection[]
  /** The save bar's name for this form ("Instructions"). */
  inlineLabel?: string
}

export type AgentEditorSection = "identity" | "model" | "toolAccess" | "instructions" | "access" | "chat"

/** New agent is four steps (Who → Model → Access → Instructions); each shows these sections. */
export const NEW_AGENT_STEPS: Array<{ label: string; sections: AgentEditorSection[] }> = [
  { label: "Who", sections: ["identity"] },
  { label: "Model", sections: ["model", "toolAccess"] },
  { label: "Access", sections: ["access"] },
  { label: "Instructions", sections: ["instructions", "chat"] },
]

/** Stands in for CreateSurface when the form is on the page. */
function InlineSurface({ children }: { children: React.ReactNode } & Record<string, unknown>) {
  return <div className="space-y-4" data-slot="agent-editor-inline">{children}</div>
}
function InlineBody({ children, className }: { children: React.ReactNode; className?: string }) {
  return <div className={className}>{children}</div>
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
  "w-full bg-background border border-foreground/[0.15] rounded-md px-2.5 py-1.5 text-[13px] text-foreground outline-none transition-colors focus:border-primary focus:ring-2 focus:ring-primary/15 coarse:min-h-[44px] coarse:text-sm"

const TOOL_PROFILES = ["MINIMAL", "CODING", "FULL"] as const

const WIZARD_HINTS = [
  "Who it is and which crew it works in.",
  "Who thinks, which program runs it, and what it may do.",
  "The account it pays with and the skills it gets.",
  "How it should behave. Then create it.",
]
/** Shared create/edit form with persistent drafts and focused settings sections. */
export function CreateAgentDialog({
  workspaceId,
  open,
  onOpenChange,
  defaultCrewSlug,
  crews: listedCrews,
  onCreated,
  agent,
  inlineSections,
  inlineLabel = "Agent",
}: CreateAgentDialogProps) {
  const inline = inlineSections !== undefined
  // Creating in the dialog is the four-step wizard; editing keeps sections.
  const wizard = !agent && !inline
  const [step, setStep] = useState(0)
  // Tool access sits with the model in the wizard and with access elsewhere.
  const show = (id: AgentEditorSection): boolean => {
    if (inline) return inlineSections.includes(id) || (id === "toolAccess" && inlineSections.includes("access"))
    if (wizard) return NEW_AGENT_STEPS[step].sections.includes(id)
    return section === id || (id === "toolAccess" && section === "access")
  }
  const crews = useMemo(() => agent?.crew && agent.crew_id && !listedCrews.some((crew) => crew.id === agent.crew_id)
    ? [...listedCrews, { ...agent.crew, id: agent.crew_id }] : listedCrews, [agent, listedCrews])
  // Upgrade lazy-loaded DiceBear styles from placeholder to real avatar.
  useAvatarStylesVersion()
  const router = useRouter()
  const [section, setSection] = useState("identity")
  // A server refusal that names a field (#2862) is shown under that field
  // rather than toasted: slug conflicts under Slug, a second lead under Role.
  const [fieldErrors, setFieldErrors] = useState<{ slug?: string; agent_role?: string }>({})
  const [providerConfirmed, setProviderConfirmed] = useState(!!agent)
  const [draft, setDraft] = useState(() => initialAgentDraft(defaultCrewSlug))
  const [persona, setPersona] = useState<string | null | undefined>(undefined)
  const [baseline, setBaseline] = useState<AgentDraft | null>(null)
  const [extra, setExtra] = useState<Record<string, unknown>>({})
  // Two writes that are not fields of the agent body: the seat the agent pays
  // with (an AGENT credential binding) and the learning posture (its own
  // audited PATCH). Held as a draft like everything else and applied on Save,
  // after the agent itself.
  const [seatDraft, setSeatDraft] = useState<LoginCredential | null>(null)
  const [learningDraft, setLearningDraft] = useState<LearningDraft | null>(null)
  // The restricted client execution profile (#3028) has its own endpoint, so
  // it is a third follow-up write: held here, applied on Save after the agent.
  const [restrictedDraft, setRestrictedDraft] = useState<RestrictedProfile | null>(null)
  const [submitting, setSubmitting] = useState(false)
  // Ref for the in-flight check inside submit() — using `submitting` state
  // there would close over a stale value and let a fast double-fire through
  // before the next render wires up the disabled button.
  const submittingRef = useRef(false)
  const [pickerOpen, setPickerOpen] = useState(false)
  // Held beside the draft rather than inside it: these are not fields of
  // POST /api/v1/agents, they are two follow-up calls the draft has no shape
  // for, and the submit-body test asserts the draft maps 1:1 to the Go struct.
  const [access, setAccess] = useState<AgentAccessSelection>({ integrationIds: [], channelIds: [] })
  // New agent: Who > Start from, and Access > Skills (assigned after create).
  const [start, setStart] = useState<AgentStart>("blank")
  const [skillDraft, setSkillDraft] = useState<WorkspaceSkillLite[]>([])
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
      const next = agent ? draftFromAgent(agent, crews) : initialAgentDraft(defaultCrewSlugRef.current)
      setSection("identity")
      setStep(0)
      setStart("blank")
      setSkillDraft([])
      setRestrictedDraft(null)
      setFieldErrors({})
      setProviderConfirmed(!!agent)
      setDraft(next)
      setBaseline(next)
      setExtra({})
      setSeatDraft(null)
      setLearningDraft(null)
      setPersona(undefined)
      setBaselineCrewSlug(defaultCrewSlugRef.current)
      setSubmitting(false)
      setAccess({ integrationIds: [], channelIds: [] })
      setRefusal(null)
    }
    wasOpenRef.current = open
  }, [open, agent, crews])

  // Auto-derive slug from name unless user has manually edited it.
  useEffect(() => {
    if (draft.slugTouched) return
    const derived = draft.name
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-|-$/g, "")
    if (derived !== draft.slug) setDraft((d) => ({ ...d, slug: derived }))
  }, [draft.name, draft.slug, draft.slugTouched])
  // A slug refusal describes the slug that was sent; any new slug (typed or
  // derived from the name) is a new question.
  useEffect(() => {
    setFieldErrors((f) => (f.slug ? { ...f, slug: undefined } : f))
  }, [draft.slug])

  const seed = draft.avatarSeed || draft.slug || draft.name || "agent"
  const requiresCrew = draft.agentRole === "LEAD"
  const finalPrompt = resolveFinalPrompt(draft)
  const isPromptFromTemplate =
    draft.selectedPersona !== null &&
    draft.editedPersonaPrompt === null &&
    !draft.customPrompt.trim()
  const learningReasonMissing = learningDraft !== null && learningDraft.reason.trim() === ""
  const valid = isIdentityValid(draft) && (!!agent || !providerConfirmed || !!draft.llmModel.trim()) && !learningReasonMissing
  // What's blocking submit? Shown to the user as an inline hint so they
  // don't have to guess why Create is disabled. Mirrors isIdentityValid
  // — keep the order matching so the hint reflects the first failing rule.
  const validationHint: string | null = (() => {
    if (valid) return null
    if (learningReasonMissing) return "Give a reason for the learning change"
    if (!agent && providerConfirmed && !draft.llmModel.trim()) return "Choose a model in Model and execution"
    const trimmedName = draft.name.trim()
    if (trimmedName.length < 2) return "Name must be at least 2 characters"
    if (trimmedName.length > 100) return "Name is too long (max 100 characters)"
    if (draft.slug.length > 50) return "Slug is too long (max 50 characters)"
    if (!/^[a-z0-9-]{2,}$/.test(draft.slug))
      return "Slug must use only lowercase letters, digits, and hyphens (2+ chars)"
    if (requiresCrew && !draft.crewSlug)
      return crews.length === 0 ? "Create a crew first — leads need one" : "Pick a crew"
    return null
  })()
  const hasNoCrews = crews.length === 0
  const crewName = crews.find((c) => c.slug === draft.crewSlug)?.name

  const handlePickPersona = useCallback((persona: AgentPersona) => {
    setDraft((d) => applyPersonaDefaults(d, persona))
    setProviderConfirmed(false)
  }, [])

  // Copy an agent: its model, runner, limits, instructions and skills; the
  // name, slug and avatar stay the new agent's own.
  const handleCopy = useCallback((src: AgentRecord) => {
    setDraft((d) => ({
      ...draftFromAgent(src, crews),
      name: d.name, slug: d.slug, slugTouched: d.slugTouched,
      avatarSeed: d.avatarSeed, avatarStyle: d.avatarStyle, avatarTouched: d.avatarTouched,
      crewSlug: d.crewSlug || (crews.find((c) => c.id === src.crew_id)?.slug ?? ""),
      agentRole: "AGENT",
    }))
    setProviderConfirmed(true)
    void apiFetch(`/api/v1/agents/${encodeURIComponent(src.id)}/skills?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : []))
      .then((rows: Array<{ skill_id: string; name?: string; skill_name?: string; display_name?: string | null }>) => {
        if (Array.isArray(rows)) setSkillDraft(rows.map((r) => ({ id: r.skill_id, name: r.skill_name ?? r.name ?? r.skill_id, display_name: r.display_name ?? null })))
      })
      .catch(() => undefined)
  }, [crews, workspaceId])

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
    if (!agent && !providerConfirmed) { setSection("model"); setStep(1); return }
    submittingRef.current = true
    setSubmitting(true)
    setRefusal(null)
    setFieldErrors({})
    try {
      const targetCrew = draft.crewSlug
        ? crews.find((c) => c.slug === draft.crewSlug) ?? null
        : null
      if ((draft.crewSlug || requiresCrew) && !targetCrew) {
        const message = draft.crewSlug
          ? `Crew "${draft.crewSlug}" no longer exists. Please reselect.`
          : "Pick a crew before creating a lead."
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
      const payload: Record<string, unknown> = agent && baseline
        ? { ...Object.fromEntries(Object.entries(body).filter(([key, value]) => JSON.stringify(value) !== JSON.stringify(agentBody(baseline, crews)[key]))), ...extra }
        : { ...body, ...extra }
      const res = await apiFetch(
        `/api/v1/agents${agent ? `/${agent.id}` : ""}?workspace_id=${encodeURIComponent(workspaceId)}`,
        {
          method: agent ? "PATCH" : "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        },
      )
      if (!res.ok) {
        throw await toApiError(res, `HTTP ${res.status}`)
      }
      const created = await res.json()
      if (agent && persona !== undefined) {
        const response = await apiFetch(`/api/v1/agents/${agent.id}/persona?workspace_id=${encodeURIComponent(workspaceId)}`, { method: persona === null ? "DELETE" : "PUT", headers: { "Content-Type": "application/json" }, body: persona === null ? undefined : JSON.stringify({ content: persona }) })
        if (!response.ok) throw new Error(`Agent settings saved, but persona could not be saved (${response.status}). Your persona draft is retained; retry Save changes.`)
      }
      // After the agent, so a refused agent change leaves the seat and the
      // posture untouched. Each is cleared once applied, so a retry after a
      // later failure does not apply it twice.
      if (agent && seatDraft) {
        try {
          await applyPaysWith(workspaceId, agent.id, seatDraft)
        } catch (err) {
          throw new Error(`Agent settings saved, but the provider account could not be changed: ${err instanceof Error ? err.message : String(err)}`)
        }
        setSeatDraft(null)
      }
      if (agent && learningDraft) {
        try {
          await saveLearning(agent.id, workspaceId, learningDraft.enabled, learningDraft.reason)
        } catch (err) {
          throw new Error(`Agent settings saved, but the learning posture could not be changed: ${err instanceof Error ? err.message : String(err)}`)
        }
        setLearningDraft(null)
      }
      if (agent && restrictedDraft) {
        try {
          await saveRestrictedProfile(agent.id, workspaceId, restrictedDraft)
        } catch (err) {
          throw new Error(`Agent settings saved, but restricted execution could not be changed: ${err instanceof Error ? err.message : String(err)}`)
        }
        setRestrictedDraft(null)
      }

      // Bindings are keyed on an agent that exists, so they are spent here
      // rather than in the body above. Failures are reported, not thrown: the
      // agent is created either way, and an agent quietly missing the tool it
      // was created for is worse than being told where to add it.
      // The agent's face is stored here, by the person who just created or
      // edited it — not by whoever happens to view it first (#2876). A PATCH
      // that left the avatar alone comes back with its avatar_url and costs
      // no request; one that changed it comes back without, and is stored.
      // Never throws: a missing render only means the face is generated.
      await storeAgentAvatar(created, workspaceId)

      const failed =
        !agent && (access.integrationIds.length || access.channelIds.length)
          ? await applyAgentAccess(workspaceId, created.id, access, accessCatalog)
          : []
      // New agent > Access: the seat it pays with and its skills, keyed on
      // the agent that now exists (credential, then skills).
      if (!agent && seatDraft) {
        try {
          await applyPaysWith(workspaceId, created.id, seatDraft)
        } catch {
          failed.push(`pays with ${seatDraft.name}`)
        }
      }
      if (!agent && skillDraft.length) failed.push(...(await assignSkills(workspaceId, created.id, skillDraft)))

      if (failed.length) {
        toast.warning(`Agent "${created.name}" created, but ${failed.length} grant${failed.length === 1 ? "" : "s"} did not apply`, {
          description: `${failed.join(", ")} — add them under the agent's Tools and access.`,
        })
      } else {
        toast.success(`Agent "${created.name}" ${agent ? "updated" : "created"}`)
      }
      if (inline) {
        // The page refetches the agent and remounts this form from it.
        onCreated(created.slug)
      } else {
        onOpenChange(false)
        onCreated(created.slug)
        router.replace(`/crews?agent=${encodeURIComponent(created.slug)}`)
      }
    } catch (err) {
      if (err instanceof ApiError && (err.field === "slug" || err.field === "agent_role")) {
        const field = err.field
        setFieldErrors({ [field]: err.message })
        setSection("identity")
        setStep(0)
        return
      }
      const message = `Could not ${agent ? "save" : "create"} agent: ${err instanceof Error ? err.message : String(err)}`
      toast.error(message)
      setRefusal(message)
    } finally {
      submittingRef.current = false
      setSubmitting(false)
    }
  }, [agent, providerConfirmed, persona, baseline, extra, seatDraft, learningDraft, restrictedDraft, draft, crews, requiresCrew, workspaceId, finalPrompt, access, accessCatalog, skillDraft, onOpenChange, onCreated, router, inline])

  // Continue on Who needs a valid identity; on Model, a provider and model.
  const stepReady = step === 0 ? isIdentityValid(draft) : step === 1 ? providerConfirmed && !!draft.llmModel.trim() : true
  const stepHint = !stepReady
    ? step === 0 ? validationHint : "Choose a provider and model"
    : null

  // ⌘↵ / Ctrl↵ is wired by the shell — this is only the "is it submittable"
  // guard the shell asks callers to keep inside their own handler.
  const handleShortcutSubmit = useCallback(() => {
    if (!valid || submitting) return
    void submit()
  }, [valid, submitting, submit])

  const dirty = agent ? persona !== undefined || JSON.stringify(draft) !== JSON.stringify(baseline) || Object.keys(extra).length > 0 || seatDraft !== null || learningDraft !== null || restrictedDraft !== null : isDraftDirty(draft, baselineCrewSlug) || Object.keys(extra).length > 0 || access.integrationIds.length > 0 || access.channelIds.length > 0 || seatDraft !== null || skillDraft.length > 0

  // On the page, one save bar commits this form with the others.
  usePageSave({
    label: inlineLabel,
    count: inline && dirty ? 1 : 0,
    canSave: valid && !submitting,
    saving: inline && submitting,
    save: () => submit(),
    discard: () => {
      if (!baseline) return
      setDraft(baseline)
      setExtra({})
      setPersona(undefined)
      setSeatDraft(null)
      setLearningDraft(null)
      setRestrictedDraft(null)
      setFieldErrors({})
      setRefusal(null)
    },
  })

  const Surface = inline ? InlineSurface : CreateSurface
  const Body = inline ? InlineBody : CreateSurfaceBody

  return (
      <Surface
        open={open}
        onOpenChange={onOpenChange}
        size={wizard ? "md" : "xl"}
        className="h-[92dvh] sm:h-[min(85dvh,720px)]"
        dirty={dirty}
        discardLabel="this agent"
        onSubmit={() => {
          // ⌘↵ inside the picker closes the picker; it must not also create
          // the agent underneath it. Same rule the crew wizard applies to
          // its base-image and icon panels.
          if (pickerOpen) { setPickerOpen(false); return }
          if (wizard && step < NEW_AGENT_STEPS.length - 1) { if (stepReady) setStep((n) => n + 1); return }
          handleShortcutSubmit()
        }}
      >
        {!inline && <CreateSurfaceHeader
          concept="crews"
          accent="purple"
          context={crewName}
          title={pickerOpen ? (agent ? `Avatar — ${agent.name}` : "Avatar — new agent") : agent ? `Edit ${agent.name}` : crewName ? `New agent in ${crewName}` : "New agent"}
          description={
            pickerOpen
              ? "Pick a style and a seed. The same seed always produces the same face."
              : agent ? "Changes are saved together when you choose Save changes." : WIZARD_HINTS[step]
          }
          onBack={pickerOpen ? () => setPickerOpen(false) : undefined}
          onClose={() => onOpenChange(false)}
        />}
        {wizard && !pickerOpen && (
          <WizardSteps
            steps={NEW_AGENT_STEPS.map((st) => st.label)}
            current={step}
            onStepChange={setStep}
            freeNavigation
            className="shrink-0 border-b border-border px-5 py-2.5"
          />
        )}
        {inline && pickerOpen && <button type="button" className="text-label text-primary-hover hover:underline" onClick={() => setPickerOpen(false)}>Done with the avatar</button>}

        <EditorLayout label="Agent editor sections" active={section} onChange={setSection} hidden={pickerOpen || inline || wizard} sections={[
          { id: "identity", label: "Identity", icon: Bot },
          { id: "model", label: "Model and execution", icon: Cpu },
          { id: "instructions", label: "Instructions and persona", icon: FileText },
          { id: "access", label: "Permissions", icon: ShieldCheck },
          { id: "chat", label: "Chat", icon: MessageSquare },
        ]}>
        <Body className="min-w-0 space-y-5">
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
            <CreateSurfaceNotice tone={requiresCrew ? "warn" : "info"} icon={TriangleAlert}>
              This workspace has <strong className="text-foreground">no crews yet</strong>. {requiresCrew
                ? "Create one before adding a Lead."
                : "This Agent will be workspace-wide; you can assign it to a crew later."}
            </CreateSurfaceNotice>
          )}

          {/* Everything below is the form. Hidden wholesale while the panel
              is up: the panel replaces the body, it does not sit beside it. */}
          {!pickerOpen && (
            <>
          <EditorPanel active={show("identity")}>
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
          {/* Start from: blank, a template (the catalogue on the step) or a
              copy of an agent. Creation only; an existing agent keeps its
              settings on its own tabs. */}
          {!agent && (
            <CreateSurfaceSection title="Start from" icon={Sparkles} accent="gold" hint="optional · pre-fills model, instructions and avatar">
              <StartFrom
                workspaceId={workspaceId}
                start={start}
                onStart={(next) => { setStart(next); if (next === "blank") handleBlank() }}
                selectedPersona={draft.selectedPersona}
                onPickPersona={handlePickPersona}
                onCopy={handleCopy}
              />
            </CreateSurfaceSection>
          )}

          {/* ─── Identity ─── */}
          <CreateSurfaceSection title="Identity" icon={Bot} accent="purple">
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
                className="mt-5 h-14 w-14 shrink-0 overflow-hidden rounded-xl border border-foreground/10 bg-muted outline-none transition-colors hover:border-primary/50 focus-visible:border-primary focus-visible:ring-2 focus-visible:ring-primary/20"
              >
                <AgentAvatar seed={seed} style={draft.avatarStyle} aria-hidden="true" className="h-full w-full rounded-none object-cover" />
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
              <CreateSurfaceField
                label="Crew"
                htmlFor="agent-crew"
                required={requiresCrew}
                hint={requiresCrew ? undefined : "Optional — leave empty for a workspace-wide Agent"}
              >
                <CrewPicker
                  id="agent-crew"
                  by="slug"
                  crews={crews}
                  value={draft.crewSlug}
                  onChange={(crewSlug) => setDraft({ ...draft, crewSlug })}
                  placeholder={requiresCrew ? "Pick crew…" : "Workspace-wide (no crew)"}
                  clearLabel={requiresCrew ? undefined : "Workspace-wide (no crew)"}
                  ariaLabel="Crew"
                />
              </CreateSurfaceField>

              <CreateSurfaceField label="Slug" htmlFor="agent-slug" hint="auto from name" error={fieldErrors.slug}>
                <input
                  id="agent-slug"
                  type="text"
                  value={draft.slug}
                  aria-invalid={fieldErrors.slug ? true : undefined}
                  aria-describedby={fieldErrors.slug ? "agent-slug-error" : undefined}
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
              <CreateSurfaceField label="Role" error={fieldErrors.agent_role}>
                <CreateSurfaceChoice
                  ariaLabel="Agent role"
                  value={draft.agentRole}
                  onChange={(agentRole) => {
                    setFieldErrors((f) => ({ ...f, agent_role: undefined }))
                    setDraft({ ...draft, agentRole })
                  }}
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

          {agent && draft.crewSlug !== baseline?.crewSlug && <CreateSurfaceNotice tone="warn">Moving this agent changes its inherited access, shared knowledge, and runtime environment.</CreateSurfaceNotice>}
          </EditorPanel>
          <EditorPanel active={show("instructions")}>
          {/* ─── Persona ─── */}
          <CreateSurfaceSection
            title="Instructions"
            icon={FileText}
            accent="purple"
            hint="how should this agent behave"
          >
            <textarea
              id="agent-persona"
              aria-label="Agent system prompt"
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
              className="w-full min-h-[140px] max-h-[260px] resize-y bg-background border border-foreground/[0.15] rounded-md px-3 py-2 text-[12px] font-mono leading-relaxed outline-none focus:border-primary focus:ring-2 focus:ring-primary/15"
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

          {agent && <PersonaDraft workspaceId={workspaceId} agentId={agent.id} value={persona} onChange={setPersona} />}
          {/* On the agent page memory is a switch on the Memory tab. */}
          {!inline && <CreateSurfaceSection title="Memory" icon={Brain} accent="purple">
            <CreateSurfaceToggleRow
              concept="memory"
              label="Memory between sessions"
              hint={
                draft.memoryEnabled
                  ? "Notes it keeps and can search later — AGENT.md, a daily journal, lessons."
                  : "Saved knowledge is not injected into runs. Conversation history and episodic recall are separate."
              }
              control={
                <Switch
                  aria-label="Memory"
                  checked={draft.memoryEnabled}
                  onCheckedChange={(next) => setDraft({ ...draft, memoryEnabled: next })}
                />
              }
            />
          </CreateSurfaceSection>}
          </EditorPanel>
          <EditorPanel active={show("model")}>
            <AgentModelSettings providerConfirmed={!!agent || providerConfirmed} onProviderConfirmed={() => setProviderConfirmed(true)} workspaceId={workspaceId} draft={draft} setDraft={setDraft} />
            {agent && (
              <RestrictedExecutionField
                value={restrictedDraft ?? (agent.restricted_execution_profile as RestrictedProfile | undefined) ?? "disabled"}
                onChange={(next) => setRestrictedDraft(next === (agent.restricted_execution_profile ?? "disabled") ? null : next)}
              />
            )}
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
                    <option value="active">Active — plans work for the crew</option>
                    <option value="passive">Passive — responds when invoked</option>
                  </select>
                </CreateSurfaceField>
              )}
          </EditorPanel>
          <EditorPanel active={show("toolAccess")}>
            <CreateSurfaceSection title="Tool access" icon={Wrench} accent="amber" hint="The runner's built-in tools. Credentials and integrations have separate permissions.">
              <CreateSurfaceChoice ariaLabel="Tool access" value={draft.toolProfile} onChange={toolProfile => setDraft({ ...draft, toolProfile })} options={TOOL_PROFILES.map(value => ({ value, label: { MINIMAL: "Read and plan", CODING: "Workspace work", FULL: "Full tool access" }[value] }))} />
              <p className="text-sm text-muted-foreground">{{ MINIMAL: "Requests a restricted tool set or read-only planning mode, depending on the runner.", CODING: "Read and edit workspace files, and run commands inside the crew container.", FULL: "All tools exposed by the runner. Crew permissions and network rules still apply." }[draft.toolProfile]}</p>
              <p className="text-xs text-muted-foreground">Enforcement depends on the selected runner. This setting does not change the crew's network access.</p>
            </CreateSurfaceSection>
          </EditorPanel>
          <EditorPanel active={show("access")}>
            {!agent && (
              <CreateSurfaceSection title="Pays with" icon={CreditCard} hint="the provider account its runs are charged to">
                <PaysWithRow workspaceId={workspaceId} agentId="" agentName={draft.name || "this agent"} cliAdapter={draft.cliAdapter} paysWith={null} pending={seatDraft} onPick={setSeatDraft} />
                <p className="text-xs text-muted-foreground">Left alone, it uses the account shared with {crewName ?? "the workspace"}. Assigned right after the agent is created.</p>
              </CreateSurfaceSection>
            )}
            {!agent && (
              <CreateSurfaceSection title="Skills" icon={Wrench} accent="amber">
                <SkillChips workspaceId={workspaceId} value={skillDraft} onChange={setSkillDraft} enabled={open && show("access")} />
              </CreateSurfaceSection>
            )}
            {!agent && <AgentAccessSection catalog={accessCatalog} selection={access} onChange={setAccess} />}
            {agent && <CreateSurfaceSection title="Provider account" icon={CreditCard}><p className="text-xs text-muted-foreground">The account changes with the rest when you choose Save changes.</p>{draft.cliAdapter !== agent.cli_adapter || draft.llmProvider !== agent.llm_provider ? <p className="text-sm text-muted-foreground">Save the new provider and runner first, then choose its account here.</p> : <PaysWithRow workspaceId={workspaceId} agentId={agent.id} agentName={agent.name} cliAdapter={agent.cli_adapter} paysWith={agent.pays_with ?? null} pending={seatDraft} onPick={setSeatDraft} />}</CreateSurfaceSection>}
            {agent && <p className="text-sm text-muted-foreground">Manage this agent&apos;s assigned skills, credentials and integrations under Tools and access.</p>}
          </EditorPanel>
          <EditorPanel active={show("chat")}>
            {wizard && (
              <CreateSurfaceSection title="What Create agent does" icon={ArrowRight}>
                <dl className="grid grid-cols-[6rem_1fr] gap-x-3 gap-y-1.5 text-sm" data-testid="new-agent-summary">
                  <dt className="text-muted-foreground">Agent</dt>
                  <dd>{[draft.name.trim() || "—", crewName ?? "workspace-wide", draft.agentRole === "LEAD" ? "lead" : null].filter(Boolean).join(" · ")}</dd>
                  <dt className="text-muted-foreground">Model</dt>
                  <dd>{providerConfirmed && draft.llmModel ? `${getModelLabel(draft.llmModel)} via ${adapterLabel(draft.cliAdapter)}` : "not chosen yet"}</dd>
                  <dt className="text-muted-foreground">Access</dt>
                  <dd>{[seatDraft ? `pays with ${seatDraft.name}` : "inherited account", skillDraft.length ? `${skillDraft.length} skill${skillDraft.length === 1 ? "" : "s"}` : null].filter(Boolean).join(" · ")}</dd>
                  <dt className="text-muted-foreground">Memory</dt>
                  <dd>{draft.memoryEnabled ? "On · daily notes merged at night" : "Off"}</dd>
                </dl>
                <p className="text-xs text-muted-foreground">Creates the agent, then assigns the account and skills: the same calls as <span className="font-mono">crewship agent create</span>, <span className="font-mono">credential binding create</span> and <span className="font-mono">skill assign</span>.</p>
              </CreateSurfaceSection>
            )}
            {!agent && <CreateSurfaceSection title="Chat suggestions and forms" icon={MessageSquare}><SuggestedPromptsField draftMode value={String(extra.suggested_prompts ?? "")} onSave={async value => { setExtra(current => ({ ...current, suggested_prompts: value })) }} /><AskFormsBuilder value={String(extra.ask_forms ?? "")} onChange={value => { setExtra(current => ({ ...current, ask_forms: value })) }} /></CreateSurfaceSection>}
            {agent && <ConfigTab agent={{ ...agent, ...extra }} patch={async body => { setExtra(current => ({ ...current, ...body })) }} learning={{ draft: learningDraft, onChange: setLearningDraft }} />}
          </EditorPanel>
            </>
          )}
        </Body>
        </EditorLayout>

        <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

        {wizard && <CreateSurfaceFooter
          // The step counter always stays; what blocks Continue / Create sits beside it.
          hint={(() => {
            const reason = step === NEW_AGENT_STEPS.length - 1 ? validationHint : stepHint
            return (
              <>
                <span data-testid="new-agent-step">Step {step + 1} of {NEW_AGENT_STEPS.length}</span>
                {reason && <> · <span className="text-warn">{reason}</span></>}
              </>
            )
          })()}
          lead={<span className="text-xs text-muted-foreground sm:hidden">Step {step + 1} of {NEW_AGENT_STEPS.length}</span>}
          onCancel={() => onOpenChange(false)}
          secondary={step > 0 ? <Button type="button" variant="ghost" size="sm" onClick={() => setStep((n) => n - 1)}>Back</Button> : undefined}
          primaryLabel={step < NEW_AGENT_STEPS.length - 1 ? "Continue" : submitting ? "Saving…" : "Create agent"}
          primaryIcon={ArrowRight}
          primaryDisabled={step < NEW_AGENT_STEPS.length - 1 ? !stepReady : !valid || !providerConfirmed}
          busy={submitting}
          onPrimary={() => (step < NEW_AGENT_STEPS.length - 1 ? setStep((n) => n + 1) : void submit())}
        />}
        {!inline && !wizard && <CreateSurfaceFooter
          hint={validationHint ? <span className="text-warn">{validationHint}</span> : undefined}
          onCancel={() => onOpenChange(false)}
          primaryLabel={submitting ? "Saving…" : agent ? "Save changes" : !providerConfirmed ? "Choose provider" : "Create agent"}
          primaryIcon={ArrowRight}
          primaryDisabled={!valid || (!agent && !providerConfirmed && section === "model")}
          busy={submitting}
          onPrimary={() => void submit()}
        />}
        {inline && validationHint && <p className="text-label text-chip-warn-fg">{validationHint}</p>}
      </Surface>
  )
}

function draftFromAgent(agent: AgentRecord, crews: CrewLite[]): AgentDraft {
  return { ...initialAgentDraft(null), name: agent.name, slug: agent.slug, slugTouched: true,
    crewSlug: crews.find((crew) => crew.id === agent.crew_id)?.slug ?? agent.crew?.slug ?? "",
    agentRole: agent.agent_role as AgentDraft["agentRole"], roleTitle: agent.role_title ?? "", description: agent.description ?? "",
    avatarSeed: agent.avatar_seed ?? "", avatarStyle: agent.avatar_style ?? DEFAULT_AVATAR_STYLE,
    customPrompt: agent.system_prompt ?? "", llmProvider: (agent.llm_provider ?? "ANTHROPIC") as AgentDraft["llmProvider"],
    llmModel: agent.llm_model ?? "", cliAdapter: agent.cli_adapter as AgentDraft["cliAdapter"],
    toolProfile: agent.tool_profile as AgentDraft["toolProfile"], timeoutSeconds: agent.timeout_seconds,
    memoryEnabled: agent.memory_enabled, leadMode: (agent.lead_mode ?? "active") as AgentDraft["leadMode"],
  }
}
function agentBody(draft: AgentDraft, crews: CrewLite[]): Record<string, unknown> {
  return { name: draft.name.trim(), slug: draft.slug.trim(), agent_role: draft.agentRole,
    crew_id: crews.find((crew) => crew.slug === draft.crewSlug)?.id ?? null,
    description: draft.description.trim() || null, role_title: draft.roleTitle.trim() || null,
    lead_mode: draft.agentRole === "LEAD" ? draft.leadMode : null, cli_adapter: draft.cliAdapter,
    llm_provider: draft.llmProvider, llm_model: draft.llmModel, system_prompt: resolveFinalPrompt(draft) || null,
    avatar_seed: draft.avatarSeed.trim() || null, avatar_style: draft.avatarStyle,
    timeout_seconds: draft.timeoutSeconds, tool_profile: draft.toolProfile, memory_enabled: draft.memoryEnabled }
}
