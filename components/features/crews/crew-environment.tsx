"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import { AlertTriangle } from "lucide-react"
import { toast } from "@/lib/toast"

import { usePageSave } from "@/components/ui/page-save-bar"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"

import { CrSegment } from "./cr-card"
import { changedCrewFields, crewToWizardState, saveCrew } from "./create-crew-dialog"
import { StepContainer, type EnvironmentSection } from "./create-crew/step-container"
import { BaseImagePanel, effectiveBaseImage, patchImage } from "./create-crew/base-image"
import type { WizardState } from "./create-crew/types"
import type { CrewRecord } from "./crew-canvas-tabs/types"

export type EnvSegment = "all" | "image" | "tools" | "versions" | "network" | "resources"

export const ENV_SEGMENTS: Array<{ id: EnvSegment; label: string; section?: EnvironmentSection }> = [
  { id: "all", label: "All" },
  { id: "image", label: "Container and image", section: "environment" },
  { id: "tools", label: "Tools", section: "tools" },
  { id: "versions", label: "Tool versions", section: "versions" },
  { id: "network", label: "Network", section: "network" },
  { id: "resources", label: "Resources", section: "limits" },
]

/** Saving any of these stops the crew container so the next run starts with them. */
const RESTARTING_FIELDS = new Set(["network_mode", "allowed_domains", "container_memory_mb", "container_cpus", "mise_config", "devcontainer_config", "runtime_image"])

export function restartsContainer(changed: Record<string, unknown>): boolean {
  return Object.keys(changed).some((k) => RESTARTING_FIELDS.has(k))
}

/**
 * C-ENV: the crew's container, tools, network and resources as one form on
 * the page (the Edit crew dialog's sections), saved through the page's one
 * save bar.
 */
export function CrewEnvironment({ workspaceId, crew, onSaved, canEdit = true }: {
  workspaceId: string
  crew: CrewRecord
  onSaved: () => void
  canEdit?: boolean
}) {
  const [segment, setSegment] = useState<EnvSegment>("all")
  const baseline = useMemo(() => crewToWizardState(crew), [crew])
  const [state, setStateFull] = useState<WizardState>(baseline)
  const [imageOpen, setImageOpen] = useState(false)
  const [imageDraft, setImageDraft] = useState<string>("")
  const [saving, setSaving] = useState(false)

  // A refetch after someone else's save replaces the baseline; a draft the
  // person has not touched follows it.
  const lastBaseline = useRef(baseline)
  useEffect(() => {
    setStateFull((prev) => (JSON.stringify(prev) === JSON.stringify(lastBaseline.current) ? baseline : prev))
    lastBaseline.current = baseline
  }, [baseline])

  const setState = (patch: Partial<WizardState>) => setStateFull((prev) => ({ ...prev, ...patch }))
  const changed = useMemo(() => {
    const all = changedCrewFields(state, baseline)
    // Identity is edited in Settings; only the environment counts here.
    for (const k of ["name", "slug", "description", "icon", "color"]) delete all[k]
    return all
  }, [state, baseline])
  const count = Object.keys(changed).length

  usePageSave({
    label: "Environment",
    count,
    canSave: canEdit,
    saving,
    save: async () => {
      setSaving(true)
      try {
        // Identity fields are taken from the baseline so this save never
        // touches what the Settings tab owns.
        await saveCrew(workspaceId, crew.id, { ...state, name: baseline.name, slug: baseline.slug, description: baseline.description, icon: baseline.icon, color: baseline.color }, baseline)
        if (restartsContainer(changed)) toast.success("Environment saved", { description: "The container restarts with it on the next run." })
        onSaved()
      } finally {
        setSaving(false)
      }
    },
    discard: () => setStateFull(baseline),
  })

  const section = ENV_SEGMENTS.find((s) => s.id === segment)?.section
  return (
    <div className="space-y-4" data-testid="crew-environment">
      <div className="overflow-x-auto">
        <CrSegment<EnvSegment> label="Environment sections" value={segment} onChange={setSegment} options={ENV_SEGMENTS.map(({ id, label }) => ({ id, label }))} />
      </div>
      <div className="flex items-start gap-2.5 rounded-card border border-border bg-card px-4 py-3 text-body text-muted-foreground">
        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-chip-warn-fg" aria-hidden />
        <span>Saving network, resources or tools restarts the container; running work is interrupted.</span>
      </div>
      <fieldset disabled={!canEdit} className="min-w-0 space-y-4 [&>div>section]:rounded-card [&>div>section]:border [&>div>section]:border-border [&>div>section]:bg-card [&>div>section]:p-4">
        <StepContainer
          state={state}
          setState={setState}
          activeSection={section}
          onPickImage={() => { setImageDraft(effectiveBaseImage(state)); setImageOpen(true) }}
        />
      </fieldset>
      {!canEdit && <p className="text-label text-muted-foreground">A manager or an admin changes the environment.</p>}

      <CreateSurface open={imageOpen} onOpenChange={setImageOpen} size="lg">
        <CreateSurfaceHeader concept="crews" context={crew.name} title="Base image" description="What the container starts from." onClose={() => setImageOpen(false)} />
        <CreateSurfaceBody>
          <BaseImagePanel value={imageDraft} onChange={setImageDraft} />
        </CreateSurfaceBody>
        <CreateSurfaceFooter
          onCancel={() => setImageOpen(false)}
          guardCancel={false}
          primaryLabel="Use this image"
          onPrimary={() => { setState(patchImage(state, imageDraft)); setImageOpen(false) }}
        />
      </CreateSurface>
    </div>
  )
}
