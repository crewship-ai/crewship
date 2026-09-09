"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { useRouter } from "next/navigation"
import { toast } from "sonner"
import { Check, Cpu, Users, HardDrive, Package, Boxes, Network } from "lucide-react"

import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceDisclosure,
  CreateSurfaceFooter,
  CreateSurfaceSection,
  CreateSurfaceHeader,
  CreateSurfacePicker,
  CreateSurfaceRefusal,
  CreateSurfaceSteps,
  type CreateSurfaceStep,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import type { CrewRecord } from "./crew-canvas-tabs/types"
import { StepIdentity } from "./create-crew/step-identity"
import { StepLineup } from "./create-crew/step-lineup"
import { EditorLayout, EditorPanel } from "./editor-layout"
import { StepContainer, type EnvironmentSection } from "./create-crew/step-container"
import { BaseImagePanel, effectiveBaseImage, patchImage } from "./create-crew/base-image"
import { ImportCrewPanel } from "./create-crew/import-panel"
import { CrewIcon } from "@/components/ui/crew-icon"
import { CREW_ICON_CATEGORIES, GRADIENT_PALETTES, getCrewIconDef, searchCrewIcons } from "@/lib/entities"
import { asCrewColor } from "./create-crew/types"
import { ProviderPicker } from "./create-agent/agent-model-settings"
import { StepReview } from "./create-crew/step-review"
import { submitCrew } from "./create-crew/submit"
import { INITIAL_STATE, type WizardState, type WizardStep } from "./create-crew/types"

export interface CreateCrewDialogProps {
  workspaceId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: () => void
  crew?: CrewRecord
}

/** Creation keeps the guided lineup flow; editing uses focused settings sections. */
const CREW_STEPS: CreateSurfaceStep[] = [
  { id: "identity", label: "Identity" },
  { id: "lineup", label: "Lineup" },
  { id: "review", label: "Review" },
]

const STEP_DESCRIPTION: Record<WizardStep, string> = {
  1: "Crews group agents that work together. Pick a recognizable icon and name.",
  2: "The agents this crew starts with. Pick a curated lineup, or stay empty and add agents later.",
  3: "The box this crew runs in — image, tooling, network. All optional; skip to defaults if unsure.",
  4: "Last look before commit. Click any section to jump back.",
}

export function CreateCrewDialog({ workspaceId, open, onOpenChange, onCreated, crew }: CreateCrewDialogProps) {
  const router = useRouter()
  const [section, setSection] = useState("identity")
  const [environmentOpen, setEnvironmentOpen] = useState(false)
  const [step, setStep] = useState<WizardStep>(1)
  const [state, setStateFull] = useState<WizardState>(INITIAL_STATE)
  const baseline = useRef<WizardState>(INITIAL_STATE)
  const wasOpen = useRef(false)
  const [busy, setBusy] = useState(false)
  // What the server said when it said no. The toast stays (a wizard that
  // closes on success wants one), but the band is the copy you can still read
  // ten seconds later, and it sits outside the scrollport.
  const [refusal, setRefusal] = useState<string | null>(null)

  useEffect(() => {
    if (open && !wasOpen.current) {
      const next: WizardState = crew ? { ...INITIAL_STATE, mode: "empty", name: crew.name, slug: crew.slug, slugTouched: true,
        description: crew.description ?? "", icon: crew.icon ?? "code", color: asCrewColor(crew.color),
        memoryMB: crew.container_memory_mb, cpus: crew.container_cpus, ttlHours: crew.container_ttl_hours,
        networkMode: crew.network_mode as WizardState["networkMode"],
        allowedDomains: Array.isArray(crew.allowed_domains) ? crew.allowed_domains : parseDomains(crew.allowed_domains),
        mcpConfig: crew.mcp_config_json ?? "", runtimeImage: crew.runtime_image ?? "", devcontainerConfig: crew.devcontainer_config ?? "", miseConfig: crew.mise_config ?? "",
      } : INITIAL_STATE
      baseline.current = next; setStateFull(next); setSection("identity"); setEnvironmentOpen(false); setStep(1); setBusy(false); setRefusal(null)
    }
    wasOpen.current = open
  }, [open, crew])

  const setState = useMemo(() => (patch: Partial<WizardState>) => {
    setStateFull((prev) => ({ ...prev, ...patch }))
  }, [])

  // Step validity gates the "Continue" button.
  const stepValid = useMemo(() => stepIsValid(step, state), [step, state])

  const lineupSummary = useMemo(() => deriveLineupSummary(state), [state])

  // What the discard guard protects. Past Step 1 there is always work to lose
  // (the lineup step auto-picks a template on mount, so state alone would say
  // "dirty" a beat later anyway); on Step 1 it is whatever has been typed.
  const dirty = useMemo(
    () => (!crew && step > 1) || JSON.stringify(state) !== JSON.stringify(baseline.current),
    [step, state, crew],
  )

  // submittingRef is a synchronous latch — `busy` is only updated on the next
  // render, so two fast clicks (or ⌘+Enter while a click is mid-flight) can
  // both observe busy=false and fire submit twice, creating duplicate crews.
  // The ref flips immediately and gates the second call before any async work.
  const submittingRef = useRef(false)

  const submit = async () => {
    if (submittingRef.current || busy || (!crew && state.mode === "browse" && !state.provider)) return
    submittingRef.current = true
    setBusy(true)
    setRefusal(null)
    try {
      const result = crew ? await saveCrew(workspaceId, crew.id, state, baseline.current) : await submitCrew(workspaceId, state)
      // applyOverrides() inside submit fires toast.warning when partial=true.
      // Suppress the success toast in that case so the user doesn't see a
      // contradictory pair ("Created" + "Some customizations didn't apply").
      if (!result.partial) {
        toast.success(`Crew "${result.name}" ${crew ? "updated" : "created"}`)
      }
      onOpenChange(false)
      onCreated()
      router.replace(`/crews?crew=${encodeURIComponent(result.slug)}`)
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setRefusal(`Could not save crew: ${message}`)
      toast.error(`Could not save crew: ${message}`)
    } finally {
      setBusy(false)
      submittingRef.current = false
    }
  }

  const advance = () => {
    if (crew || step === 4) {
      submit()
      return
    }
    setStep(step === 2 ? 4 : 2)
  }

  // Inert rather than absent while a create is in flight: the header's back
  // arrow has no disabled state (absent means "no arrow", not "a dead one"),
  // and making it vanish for the length of a POST shifts the title sideways.
  const back = () => {
    if (busy) return
    if (step > 1) setStep(step === 4 ? 2 : 1)
  }

  // Auto-focus the primary action when the user lands on Review (Step 5) so
  // ⌘+Enter is unambiguous and screen readers announce "Create crew" first.
  // Step 1's Name input keeps its inline `autoFocus` (mounts fresh each entry).
  //
  // It matters more than it used to: ⌘↵ is now the shell's, wired on the
  // dialog rather than on `window`, so it needs focus to be somewhere inside
  // the surface — and the click that lands you on Review ("Skip to defaults")
  // unmounts the very button that had it. CreateSurfaceFooter exposes no ref
  // to its primary, hence the query: the primary is its last button.
  // The base-image picker, as a panel this surface swaps to rather than a
  // second dialog over it.
  const [panel, setPanel] = useState<null | "image" | "icon" | "import">(null)
  const [iconQuery, setIconQuery] = useState("")
  const [iconCategory, setIconCategory] = useState<string | null>(null)
  const iconResults = useMemo(
    () => searchCrewIcons(iconCategory ?? iconQuery),
    [iconQuery, iconCategory],
  )

  const footerRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (step === 4 && !busy) {
      const buttons = footerRef.current?.querySelectorAll("button")
      buttons?.[buttons.length - 1]?.focus()
    }
  }, [step, busy])

  return (
    <CreateSurface
      open={open}
      onOpenChange={onOpenChange}
      size={crew ? "xl" : "lg"}
      className={crew ? "h-[92dvh] sm:h-[min(85dvh,720px)]" : undefined}
      dirty={dirty}
      discardLabel="this crew"
      onSubmit={() => {
        // ⌘↵ inside the picker closes the picker; it must not also advance the
        // step underneath it.
        if (panel) { setPanel(null); return }
        if (stepValid) advance()
      }}
    >
      {/* The base-image catalogue is a PANEL, not a list on the step: the
          surface swaps its header, body and footer for it and the back arrow
          returns. Same shape as the icon picker, and the reason a nine-item
          catalogue fits on a step that also carries tooling, network and
          sizing. */}
      <CreateSurfaceHeader
        concept="crews"
        context="Crews"
        title={
          panel === "image"
            ? `Base image — ${crew ? crew.name : "new crew"}`
            : panel === "icon"
              ? `Icon — ${crew ? crew.name : "new crew"}`
              : panel === "import"
                ? "Import — new crew"
                : crew ? `Edit ${crew.name}` : "New crew"
        }
        description={
          panel === "image"
            ? "What the container starts from. Node 22 is the recommendation for most agent work; the rest are there for a crew that needs a toolchain preinstalled."
            : panel === "icon"
              ? "Pick a colour, then an icon. Browse by category, or search."
              : panel === "import"
                ? "Read a crew manifest into this form. It fills in what the wizard asks about and tells you what it leaves behind."
                : crew ? "Changes are saved together. Existing agents remain in the crew." : STEP_DESCRIPTION[step]
        }
        onBack={panel ? () => setPanel(null) : step > 1 ? back : undefined}
        onClose={() => onOpenChange(false)}
        meta={
          panel || crew ? undefined : (
            <span className="max-sm:hidden">
              {step === 4 ? "ready to create" : `step ${step} of 3`}
            </span>
          )
        }
      />

      {/* The kit's step strip, which draws its own landmark — this used to
          wrap it in a second <nav>. Hidden inside a panel: the panel is not a
          step, and a strip saying "3 of 4" over a picker is a lie about where
          you are. */}
      {!panel && !crew && (
        <CreateSurfaceSteps
          ariaLabel="Wizard progress"
          steps={CREW_STEPS}
          current={step === 4 ? 2 : step - 1}
          onJump={(i) => setStep(i === 2 ? 4 : (i + 1) as WizardStep)}
        />
      )}

      <EditorLayout label="Crew editor sections" active={section} onChange={setSection} hidden={!crew || !!panel} sections={[
        { id: "identity", label: "Identity", icon: Users },
        { id: "environment", label: "Environment", icon: HardDrive },
        { id: "tools", label: "Tools", icon: Package },
        { id: "versions", label: "Tool versions", icon: Boxes },
        { id: "network", label: "Network", icon: Network },
        { id: "limits", label: "Resource limits", icon: Cpu },
      ]}>
      <CreateSurfaceBody className="min-w-0 space-y-5 [&>section]:rounded-xl [&>section]:border [&>section]:border-border/60 [&>section]:bg-card [&>section]:p-4">
        {panel === "image" && (
          <BaseImagePanel
            value={effectiveBaseImage(state)}
            onChange={(image) => setState(patchImage(state, image))}
          />
        )}
        {panel === "icon" && (
          <CreateSurfacePicker
            preview={<CrewIcon icon={state.icon} color={state.color} size="xl" />}
            previewHint={`${getCrewIconDef(state.icon).label} · ${state.color}`}
            palette={{
              value: state.color,
              onChange: (id) => setState({ color: asCrewColor(id) }),
              options: GRADIENT_PALETTES.map((g) => ({ id: g.id, dot: g.dot })),
            }}
            categories={{
              value: iconCategory,
              options: CREW_ICON_CATEGORIES,
              onChange: (c) => { setIconCategory(c); setIconQuery("") },
            }}
            search={{
              value: iconQuery,
              onChange: (v) => { setIconQuery(v); setIconCategory(null) },
              placeholder: "Search icons…",
            }}
            options={iconResults.map((name) => {
              const def = getCrewIconDef(name)
              return { id: name, label: def.label, render: <def.icon className="h-4 w-4 text-foreground/70" /> }
            })}
            value={state.icon}
            onChange={(icon) => setState({ icon })}
            columns={8}
          />
        )}
        {panel === "import" && (
          <ImportCrewPanel
            onApply={(patch) => {
              setState(patch)
              setPanel(null)
              // Back to Identity, not on to Container. The import rewrote the
              // name, slug, icon and colour, and the step that shows those is
              // step 1 — landing anywhere else means the one thing the user
              // most needs to check is the one thing they walked past.
              setStep(1)
              toast.success("Form filled from the manifest")
            }}
          />
        )}
        {!panel && step === 1 && (
          <EditorPanel active={!crew || section === "identity"}><StepIdentity state={state} setState={setState} onPickIcon={() => setPanel("icon")} /></EditorPanel>
        )}
        {!panel && !crew && step === 2 && (
          <StepLineup
            state={state}
            setState={setState}
            workspaceId={workspaceId}
            onImport={() => setPanel("import")}
          />
        )}
        {!panel && crew && <div hidden={section === "identity"}><StepContainer state={state} setState={setState} activeSection={(section === "identity" ? "environment" : section) as EnvironmentSection} onPickImage={() => setPanel("image")} /></div>}
        {!panel && !crew && step === 4 && state.mode === "browse" && <CreateSurfaceSection title="Model provider" icon={Cpu} accent="teal" hint="Choose the provider for this team's agents. Its matching runner is installed automatically; connect an account before the first run.">
          <ProviderPicker value={state.provider} onChange={provider => setState({ provider })} />
        </CreateSurfaceSection>}
        {!panel && !crew && (step === 4 || step === 3) && (
          <CreateSurfaceDisclosure key={String(environmentOpen)} label="Environment and runtime" icon={Cpu} accent="teal" defaultOpen={environmentOpen || step === 3}>
            <StepContainer state={state} setState={setState} onPickImage={() => { setEnvironmentOpen(true); setPanel("image") }} />
          </CreateSurfaceDisclosure>
        )}
        {!panel && step === 4 && (
          <StepReview
            state={state}
            onEdit={(s) => { if (s >= 3) setEnvironmentOpen(true); setStep(s >= 3 ? 4 : s) }}
            lineupSummary={lineupSummary}
          />
        )}
      </CreateSurfaceBody>
      </EditorLayout>

      <CreateSurfaceRefusal message={refusal} onDismiss={() => setRefusal(null)} />

      <div ref={footerRef} className="shrink-0">
        <CreateSurfaceFooter
          // No step counter here. CreateSurfaceSteps already states the
          // position twice over — the numbered chips on a pointer device, and
          // "3 / 4" beside a progress bar on a phone — and the header's meta
          // says it a third time. The footer's job is the keyboard hint.
          hint={
            panel
              ? undefined
              : crew ? "⌘+Enter to save · Esc cancel"
              : step === 4
                ? state.mode === "browse" && !state.provider ? "Choose a provider for the agents" : "⌘+Enter to confirm · Esc cancel"
                : "⌘+Enter to continue"
          }
          // Inside the panel, Cancel means "back out of the panel" — the same
          // rule the project modal's icon panel follows, and the reason
          // guardCancel is off there: nothing is discarded by leaving a picker.
          onCancel={panel ? () => setPanel(null) : () => onOpenChange(false)}
          guardCancel={!panel}
          cancelLabel={panel ? "Back" : "Cancel"}
          primaryLabel={
            panel === "image"
              ? "Use this image"
              : panel === "icon"
                ? "Use this icon"
                : crew ? (busy ? "Saving…" : "Save changes") : step === 4
                  ? (busy ? "Creating…" : "Create crew")
                  : "Continue"
          }
          primaryIcon={!panel && step === 4 ? Check : undefined}
          onPrimary={panel ? () => setPanel(null) : advance}
          primaryDisabled={panel ? false : !stepValid}
          busy={panel ? false : busy}
        />
      </div>
    </CreateSurface>
  )
}

// =============================================================================
// Helpers
// =============================================================================

const SLUG_RE = /^[a-z0-9][a-z0-9-]*[a-z0-9]$/

function stepIsValid(step: WizardStep, s: WizardState): boolean {
  if (step === 1) {
    return s.name.trim().length >= 2 && s.slug.trim().length >= 2 && SLUG_RE.test(s.slug)
  }
  if (step === 2) {
    if (s.mode === "browse") return !!s.pickedTemplateSlug
    return true // empty
  }
  if (step === 4 && s.mode === "browse") return !!s.provider
  // step === 3 (Container) is always valid: image and tooling are optional,
  // an empty allowlist still permits provider APIs and platform connections, and the
  // sizing chips cannot produce a zero — CustomNumberChip refuses anything
  // outside [MIN, MAX] and keeps the previous value.
  return true
}

function deriveLineupSummary(s: WizardState): { count: number; source: string; agents?: { name: string; agent_role: string }[] } {
  if (s.mode === "browse" && s.pickedTemplateMeta) {
    return {
      count: s.pickedTemplateMeta.agentCount,
      source: `template: ${s.pickedTemplateMeta.name}`,
      agents: s.pickedTemplateMeta.agents,
    }
  }
  return { count: 0, source: "empty" }
}

function parseDomains(value: string | null): string[] {
  try { const parsed: unknown = JSON.parse(value || "[]"); return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === "string") : [] } catch { return [] }
}
function crewBody(state: WizardState): Record<string, unknown> {
  return { name: state.name.trim(), slug: state.slug.trim(), description: state.description.trim(), icon: state.icon, color: state.color,
    container_memory_mb: state.memoryMB, container_cpus: state.cpus, container_ttl_hours: state.ttlHours ?? 0,
    network_mode: state.networkMode, allowed_domains: state.allowedDomains, runtime_image: state.runtimeImage,
    devcontainer_config: state.devcontainerConfig, mise_config: state.miseConfig, mcp_config_json: state.mcpConfig }
}
async function saveCrew(workspaceId: string, id: string, state: WizardState, baseline: WizardState) {
  const old = crewBody(baseline)
  const body = Object.fromEntries(Object.entries(crewBody(state)).filter(([key, value]) => JSON.stringify(value) !== JSON.stringify(old[key])))
  const response = await apiFetch(`/api/v1/crews/${id}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) })
  if (!response.ok) throw new Error(`Crew could not be saved (${response.status}).`)
  return response.json() as Promise<{ id: string; name: string; slug: string; partial?: boolean }>
}
