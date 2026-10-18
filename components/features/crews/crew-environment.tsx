"use client"

import { useEffect, useMemo, useRef, useState } from "react"
import dynamic from "next/dynamic"
import { Code, FolderOpen, Info, Play, Plus, RefreshCw, Square, Terminal, X } from "lucide-react"
import { toast } from "@/lib/toast"

import { Button } from "@/components/ui/button"
import { ConfirmDialog } from "@/components/ui/confirm-dialog"
import { StatusPill } from "@/components/ui/status-pill"
import { Switch } from "@/components/ui/switch"
import { toastSwitchError, usePageSave } from "@/components/ui/page-save-bar"
import {
  CreateSurface,
  CreateSurfaceBody,
  CreateSurfaceFooter,
  CreateSurfaceHeader,
} from "@/components/layout/create-surface"
import { apiFetch } from "@/lib/api-fetch"
import { toApiError } from "@/lib/api-error"
import { cn } from "@/lib/utils"

import { CrCard, CrKv, CrLine, CrNote, CrSegment } from "./cr-card"
import { changedCrewFields, crewToWizardState, saveCrew } from "./create-crew-dialog"
import { NetworkSection, SizeDisclosure } from "./create-crew/step-container"
import { BaseImagePanel, effectiveBaseImage, patchImage } from "./create-crew/base-image"
import { CrewImageFreshness } from "./crew-image-freshness"
import { CrewServiceQuotas } from "./crew-service-quotas"
import { CrewServiceLifecycle } from "./crew-service-lifecycle"
import { CrewMCPConfig } from "./crew-mcp-config"
import { ConfigTextareaEditor } from "./config-textarea-editor"
import { RuntimeSecurityConfig } from "./runtime-security-config"
import { envSecurity, envSummary, withSecurity, withoutFeature, withoutTool } from "./crew-env-config"
import { featureRefToTool } from "./runtime-config-brands"
import type { RuntimeConfigValue } from "./runtime-config"
import type { WizardState } from "./create-crew/types"
import type { CrewRecord } from "./crew-canvas-tabs/types"
import type { ContainerStatus } from "./bottom-panel/types"

// The catalogue browsers are big (1 300-row feature catalogue); they load
// only when Add a tool / Add a feature opens.
const RuntimeConfig = dynamic(() => import("./runtime-config").then((m) => m.RuntimeConfig), {
  ssr: false,
  loading: () => <p className="py-6 text-center text-label text-muted-foreground" role="status">Loading the catalogue…</p>,
})

export type EnvSegment = "all" | "container" | "tools" | "network" | "services" | "advanced"

export const ENV_SEGMENTS: Array<{ id: EnvSegment; label: string }> = [
  { id: "all", label: "All" },
  { id: "container", label: "Container and image" },
  { id: "tools", label: "Tools" },
  { id: "network", label: "Network" },
  { id: "services", label: "Services" },
  { id: "advanced", label: "Advanced" },
]

/** Saving any of these stops the crew container so the next run starts with them. */
const RESTARTING_FIELDS = new Set(["network_mode", "allowed_domains", "container_memory_mb", "container_cpus", "mise_config", "devcontainer_config", "runtime_image"])

export function restartsContainer(changed: Record<string, unknown>): boolean {
  return Object.keys(changed).some((k) => RESTARTING_FIELDS.has(k))
}

const FOLDERS: Array<[string, string]> = [
  ["/crew", "Crew files: agents/<slug> per agent, shared/ for all"],
  ["/workspace", "Working copy of repositories"],
  ["/output", "What runs produce"],
  ["/home/agent", "Home, survives restarts"],
  ["/opt/crew-tools", "Installed tools, survive restarts"],
  ["/secrets", "In memory only, filled by the credential proxy"],
]

interface PortExposure { id: string; agent_slug: string; container_port: number; description?: string; status: string; expires_at: string }
interface Revision { id: string; build_hash: string; image_id: string; created_at: string }

/**
 * C-ENV: the crew's container, image, tools, network, services and the
 * advanced settings as cards, filtered by a segment. Typed values (image,
 * tools, features, network, size, security, environment variables) are one
 * draft saved through the page's save bar; switches and actions (Start/Stop,
 * Rebuild, Private network, Revoke) apply on the spot.
 */
export function CrewEnvironment({
  workspaceId,
  crew,
  onSaved,
  canEdit = true,
  isAdmin = false,
  containers = null,
  runningAgents = 0,
  onRestartAgents,
  onOpenTerminal,
  onOpenFiles,
  onContainersChanged,
  patch,
}: {
  workspaceId: string
  crew: CrewRecord
  onSaved: () => void
  /** MANAGER+: runtime edits, start/stop, rebuild (roleCreate on the server). */
  canEdit?: boolean
  /** OWNER/ADMIN: privileged mode, private network, service quotas. */
  isAdmin?: boolean
  containers?: ContainerStatus[] | null
  runningAgents?: number
  onRestartAgents?: () => void
  onOpenTerminal?: () => void
  onOpenFiles?: () => void
  onContainersChanged?: () => void
  /** PATCH /crews/{id} for the switches that apply at once. */
  patch?: (body: Record<string, unknown>) => Promise<void>
}) {
  const ws = encodeURIComponent(workspaceId)
  const [segment, setSegment] = useState<EnvSegment>("all")
  const baseline = useMemo(() => crewToWizardState(crew), [crew])
  const [state, setStateFull] = useState<WizardState>(baseline)
  const [saving, setSaving] = useState(false)
  const [dialog, setDialog] = useState<null | "image" | "tool" | "feature" | "rebuild" | "stop">(null)
  const [imageDraft, setImageDraft] = useState("")
  const [beforeDialog, setBeforeDialog] = useState<WizardState | null>(null)
  const [rawMise, setRawMise] = useState(false)

  // A refetch after someone else's save replaces the baseline; a draft the
  // person has not touched follows it.
  const lastBaseline = useRef(baseline)
  useEffect(() => {
    setStateFull((prev) => (JSON.stringify(prev) === JSON.stringify(lastBaseline.current) ? baseline : prev))
    lastBaseline.current = baseline
  }, [baseline])

  const setState = (p: Partial<WizardState>) => setStateFull((prev) => ({ ...prev, ...p }))
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

  const summary = envSummary(state.devcontainerConfig, state.miseConfig)
  const security = envSecurity(state.devcontainerConfig)
  const runtime = containers?.find((c) => c.kind === "crew") ?? null
  const running = runtime?.status === "running"
  const show = (s: EnvSegment) => segment === "all" || segment === s

  const openPicker = (kind: "tool" | "feature") => { setBeforeDialog(state); setDialog(kind) }
  const cancelPicker = () => { if (beforeDialog) setStateFull(beforeDialog); setBeforeDialog(null); setDialog(null) }
  const updateRuntime = (value: RuntimeConfigValue) => setState({ runtimeImage: value.runtimeImage, devcontainerConfig: value.devcontainerConfig, miseConfig: value.miseConfig })

  const containerAction = async (verb: "start" | "stop") => {
    const r = await apiFetch(`/api/v1/crews/${crew.id}/container-${verb}?workspace_id=${ws}`, { method: "POST" })
    if (!r.ok) {
      const err = await toApiError(r, `HTTP ${r.status}`)
      toast.error(verb === "stop" ? "The container did not stop" : "The container did not start", { description: err.message })
      throw err
    }
    toast.success(verb === "stop" ? "Container stopped · it starts again on the next run" : "Starting the container…")
    onContainersChanged?.()
  }

  const flipPrivateNetwork = async (next: boolean) => {
    if (!patch) return
    try {
      await patch({ allow_private_endpoints: next })
      toast.success(next ? "Private network allowed" : "Private network blocked", { description: "The container picks it up on the next run." })
    } catch (err) {
      toastSwitchError("Private network", err instanceof Error ? err.message : String(err), () => void flipPrivateNetwork(next))
    }
  }

  return (
    <div className="space-y-4" data-testid="crew-environment">
      <div className="flex flex-wrap items-center gap-3">
        <div className="max-w-full overflow-x-auto">
          <CrSegment<EnvSegment> label="Environment sections" value={segment} onChange={setSegment} options={ENV_SEGMENTS} />
        </div>
        <span className="flex min-w-0 flex-1 items-start gap-1.5 text-label text-muted-foreground">
          <Info className="mt-0.5 size-3.5 shrink-0" aria-hidden />
          Saving network, resources or tools restarts the container; running work is interrupted.
        </span>
      </div>
      {!canEdit && <p className="text-label text-muted-foreground">A manager or an admin changes the environment.</p>}

      <div className="grid gap-4 xl:grid-cols-2">
        {show("container") && (
          <CrCard title="Container" concept="crews" data-testid="env-container">
            <CrKv rows={[
              ["State", runtime ? <StatusPill className="font-sans" status={runtime.status} live={running} /> : <span className="text-muted-foreground">Not created yet · starts on the next run</span>],
              ["Stops when", crew.container_ttl_hours ? `Idle for ${crew.container_ttl_hours} h` : "Never"],
              ["Size", `${state.memoryMB} MB · ${state.cpus} CPU`],
            ]} />
            {(containers ?? []).map((c) => (
              <CrLine
                key={c.name}
                title={c.kind === "crew" ? "Agents" : c.name}
                sub={<span className="font-mono">{c.image}</span>}
                right={<span className="text-label text-muted-foreground">{c.status === "running" ? [c.cpu_percent != null ? `${Math.round(c.cpu_percent)} %` : null, c.memory_mb != null ? `${c.memory_mb} MB` : null].filter(Boolean).join(" · ") || "running" : c.status}</span>}
              />
            ))}
            <div className="flex flex-wrap gap-2 border-t border-border px-4 py-3">
              {canEdit && (running
                ? <Button variant="outline" size="sm" onClick={() => setDialog("stop")}><Square className="size-3.5" aria-hidden /> Stop container</Button>
                : <Button variant="outline" size="sm" onClick={() => void containerAction("start").catch(() => undefined)}><Play className="size-3.5" aria-hidden /> Start container</Button>)}
              {canEdit && onRestartAgents && <Button variant="ghost" size="sm" onClick={onRestartAgents}><RefreshCw className="size-3.5" aria-hidden /> Restart agents</Button>}
              {onOpenTerminal && <Button variant="ghost" size="sm" onClick={onOpenTerminal}><Terminal className="size-3.5" aria-hidden /> Open terminal</Button>}
            </div>
          </CrCard>
        )}

        {show("container") && (
          <CrCard
            title="Image"
            concept="crews"
            data-testid="env-image"
            action={canEdit ? <Button variant="ghost" size="sm" onClick={() => { setImageDraft(effectiveBaseImage(state)); setDialog("image") }}>Change</Button> : undefined}
          >
            <CrLine title={<span className="font-mono">{effectiveBaseImage(state)}</span>} sub="What the container is built from; tools and features go on top." />
            <div className="border-t border-border px-4 py-3">
              <CrewImageFreshness crewId={crew.id} workspaceId={workspaceId} canEdit={canEdit} />
            </div>
            <div className="flex flex-wrap gap-2 border-t border-border px-4 py-3">
              {canEdit && <Button variant="ghost" size="sm" onClick={() => setDialog("rebuild")}><RefreshCw className="size-3.5" aria-hidden /> Rebuild…</Button>}
            </div>
            <BuildHistory workspaceId={workspaceId} crewId={crew.id} />
          </CrCard>
        )}

        {show("tools") && (
          <CrCard
            title="Tools and versions"
            count={summary.tools.length || undefined}
            concept="crews"
            data-testid="env-tools"
            action={canEdit ? (
              <span className="flex items-center gap-1">
                <Button variant="outline" size="sm" onClick={() => openPicker("tool")} disabled={!summary.miseVisual}><Plus className="size-3.5" aria-hidden /> Add a tool</Button>
                <Button variant="ghost" size="sm" aria-expanded={rawMise} onClick={() => setRawMise((o) => !o)}><Code className="size-3.5" aria-hidden /> Edit as text</Button>
              </span>
            ) : undefined}
          >
            {!summary.miseVisual && <CrNote>This crew’s tool versions are written by hand; edit them as text.</CrNote>}
            {summary.miseVisual && summary.tools.length === 0 && <CrNote>No pinned versions. Add one when a version matters (Node 22.11.0, not just Node).</CrNote>}
            {summary.tools.map(([tool, version]) => (
              <CrLine
                key={tool}
                title={tool}
                sub={<>mise · <span className="font-mono">{version}</span> · exact version pinned in the crew lock</>}
                right={canEdit ? <Button variant="ghost" size="icon-sm" aria-label={`Remove ${tool}`} onClick={() => setState({ miseConfig: withoutTool(state.miseConfig, tool) })}><X className="size-3.5" /></Button> : undefined}
              />
            ))}
            {rawMise && (
              <div className="border-t border-border p-3">
                <ConfigTextareaEditor format="json" filename="mise config" value={state.miseConfig || null} readOnly={!canEdit} onSave={(next) => setState({ miseConfig: next ?? "" })} hint="Saved with the rest of the environment." />
              </div>
            )}
          </CrCard>
        )}

        {show("tools") && (
          <CrCard
            title="Features"
            count={summary.features.length || undefined}
            concept="crews"
            data-testid="env-features"
            action={canEdit ? <Button variant="outline" size="sm" onClick={() => openPicker("feature")}><Plus className="size-3.5" aria-hidden /> Add a feature</Button> : undefined}
          >
            {summary.features.length === 0 && <CrNote>Only what the base image has.</CrNote>}
            {summary.features.map((ref) => (
              <CrLine
                key={ref}
                title={featureRefToTool(ref)}
                sub={<span className="font-mono">{ref.replace(/^ghcr\.io\//, "")}</span>}
                right={canEdit ? <Button variant="ghost" size="icon-sm" aria-label={`Remove ${featureRefToTool(ref)}`} onClick={() => setState({ devcontainerConfig: withoutFeature(state.devcontainerConfig, ref) })}><X className="size-3.5" /></Button> : undefined}
              />
            ))}
          </CrCard>
        )}

        {show("network") && (
          <CrCard title="Network" concept="crews" data-testid="env-network">
            <fieldset disabled={!canEdit} className="min-w-0 border-t border-border p-3 [&>section]:border-0 [&>section]:p-0">
              <NetworkSection state={state} setState={setState} />
            </fieldset>
            <CrLine
              title="Private network"
              sub="10.x, 192.168.x and localhost services; cloud metadata stays blocked · admins only"
              right={<Switch aria-label="Private network" checked={crew.allow_private_endpoints ?? false} disabled={!isAdmin || !patch} onCheckedChange={(v) => void flipPrivateNetwork(v)} />}
            />
            <CrLine
              title="Enforced"
              sub={crew.network_mode_enforced === false ? crew.network_mode_unenforced_reason || "Not enforced on this host: the container runtime has no egress fence" : "The egress proxy blocks everything else"}
              right={<StatusPill className="font-sans" status={crew.network_mode_enforced === false ? "not_enforced" : "enforced"} tone={crew.network_mode_enforced === false ? "warn" : "success"} label={crew.network_mode_enforced === false ? "No" : "Yes"} />}
            />
          </CrCard>
        )}

        {show("network") && (
          <CrCard title="Resources" concept="crews" data-testid="env-resources">
            <fieldset disabled={!canEdit} className="min-w-0 border-t border-border p-3">
              <SizeDisclosure state={state} setState={setState} defaultOpen />
            </fieldset>
          </CrCard>
        )}

        {show("services") && (
          <CrCard title="Services next to the agents" concept="crews" className="xl:col-span-2" data-testid="env-services">
            <div className="space-y-4 border-t border-border p-3">
              <CrewServiceLifecycle key={crew.id} crewId={crew.id} workspaceId={workspaceId} canManage={canEdit} />
              {patch && <CrewServiceQuotas key={`quotas-${crew.id}`} servicesJSON={crew.services_json} canManage={isAdmin} save={patch} />}
              <p className="text-label text-muted-foreground">Image, ports and environment of a service come from crewship.yaml (<span className="font-mono">services:</span>). Services are reachable only from this crew.</p>
            </div>
          </CrCard>
        )}

        {show("services") && <PreviewLinks workspaceId={workspaceId} crewId={crew.id} canRevoke={canEdit} />}

        {show("advanced") && (
          <CrCard title="Environment variables" count={summary.env.length || undefined} concept="crews" data-testid="env-vars">
            <fieldset disabled={!canEdit} className="min-w-0 border-t border-border p-3 [&_section]:border-0 [&_section]:p-0">
              <RuntimeSecurityConfig parts={["env"]} value={security} onChange={(next) => setState({ devcontainerConfig: withSecurity(state.devcontainerConfig, next) })} />
            </fieldset>
            <CrNote>Secrets do not belong here: share a credential instead, it never lands on disk.</CrNote>
          </CrCard>
        )}

        {show("advanced") && (
          <CrCard title="Security" concept="crews" data-testid="env-security">
            <fieldset disabled={!canEdit} className="min-w-0 space-y-3 border-t border-border p-3">
              <RuntimeSecurityConfig parts={["isolation", "capabilities", "mounts", "hook"]} value={security} canEditPrivileged={isAdmin} onChange={(next) => setState({ devcontainerConfig: withSecurity(state.devcontainerConfig, next) })} />
            </fieldset>
          </CrCard>
        )}

        {show("advanced") && (
          <CrCard
            title="Folders in the container"
            concept="crews"
            data-testid="env-folders"
            action={onOpenFiles ? <Button variant="ghost" size="sm" onClick={onOpenFiles}><FolderOpen className="size-3.5" aria-hidden /> Open files</Button> : undefined}
          >
            {FOLDERS.map(([path, what]) => <CrLine key={path} title={<span className="font-mono">{path}</span>} sub={what} />)}
          </CrCard>
        )}

        {show("advanced") && (
          <CrCard title="MCP servers" concept="integrations" data-testid="env-mcp">
            <div className="border-t border-border p-3">
              <CrewMCPConfig crewId={crew.id} workspaceId={workspaceId} />
            </div>
          </CrCard>
        )}
      </div>

      {/* D-CRIMG */}
      <CreateSurface open={dialog === "image"} onOpenChange={(o) => { if (!o) setDialog(null) }} size="lg">
        <CreateSurfaceHeader concept="crews" context={crew.name} title="Base image" description="What the crew container is built from. Tools and features are added on top." onClose={() => setDialog(null)} />
        <CreateSurfaceBody>
          <BaseImagePanel value={imageDraft} onChange={setImageDraft} />
          <p className="mt-3 text-label text-muted-foreground">Changing it rebuilds the container when you save.</p>
        </CreateSurfaceBody>
        <CreateSurfaceFooter onCancel={() => setDialog(null)} guardCancel={false} primaryLabel="Use this image" onPrimary={() => { setState(patchImage(state, imageDraft)); setDialog(null) }} />
      </CreateSurface>

      {/* D-CRTOOL / D-CRFEAT: the catalogue, edits flow into the draft; Cancel puts it back. */}
      <CreateSurface open={dialog === "tool" || dialog === "feature"} onOpenChange={(o) => { if (!o) cancelPicker() }} size="md">
        <CreateSurfaceHeader
          concept="crews"
          context={crew.name}
          title={dialog === "tool" ? "Add a tool" : "Add a feature"}
          description={dialog === "tool" ? "From the mise registry; exact versions are pinned in the crew lock." : "Dev container features from containers.dev."}
          onClose={cancelPicker}
        />
        <CreateSurfaceBody>
          {(dialog === "tool" || dialog === "feature") && (
            <RuntimeConfig
              value={{ runtimeImage: state.runtimeImage, devcontainerConfig: state.devcontainerConfig, miseConfig: state.miseConfig }}
              onChange={updateRuntime}
              layout="sections"
              hideBaseImage
              focusedSection={dialog === "tool" ? "versions" : "tools"}
              browserHeight="360px"
            />
          )}
        </CreateSurfaceBody>
        <CreateSurfaceFooter onCancel={cancelPicker} guardCancel={false} primaryLabel="Done" onPrimary={() => { setBeforeDialog(null); setDialog(null) }} hint="Changes apply when you save the environment." />
      </CreateSurface>

      <RebuildDialog open={dialog === "rebuild"} onOpenChange={(o) => { if (!o) setDialog(null) }} workspaceId={workspaceId} crewId={crew.id} crewName={crew.name} />

      <ConfirmDialog
        open={dialog === "stop"}
        onOpenChange={(o) => { if (!o) setDialog(null) }}
        title={`Stop the ${crew.name} container?`}
        description="Agents in this crew stop where they are. The container starts again on the next run."
        consequences={[
          { tone: "lost", text: `${runningAgents} running agent${runningAgents === 1 ? " stops" : "s stop"} mid-run` },
          { tone: "kept", text: "Files in /crew, memory and run history stay" },
        ]}
        confirmLabel="Stop container"
        onConfirm={() => containerAction("stop")}
      />
    </div>
  )
}

/** D-CRREBUILD: a build that reuses cached layers, or one from scratch. */
export function RebuildDialog({ open, onOpenChange, workspaceId, crewId, crewName }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaceId: string
  crewId: string
  crewName: string
}) {
  const [mode, setMode] = useState<"provision" | "rebuild">("provision")
  const [busy, setBusy] = useState(false)
  useEffect(() => { if (open) setMode("provision") }, [open])

  const build = async () => {
    setBusy(true)
    try {
      const r = await apiFetch(`/api/v1/crews/${crewId}/${mode}?workspace_id=${encodeURIComponent(workspaceId)}`, { method: "POST" })
      if (!r.ok) throw await toApiError(r, `HTTP ${r.status}`)
      toast.success(mode === "provision" ? `Building ${crewName}’s image · reuses cached layers` : `Rebuilding ${crewName}’s image from scratch`)
      onOpenChange(false)
    } catch (err) {
      toast.error("The build did not start", { description: err instanceof Error ? err.message : String(err) })
    } finally {
      setBusy(false)
    }
  }

  const options: Array<{ id: "provision" | "rebuild"; title: string; hint: string }> = [
    { id: "provision", title: "Build changes", hint: "Reuses cached layers; only what changed is built again" },
    { id: "rebuild", title: "Rebuild from scratch", hint: "Downloads and installs everything again, without the cache" },
  ]
  return (
    <CreateSurface open={open} onOpenChange={onOpenChange} size="sm" onSubmit={() => void build()}>
      <CreateSurfaceHeader concept="crews" context={crewName} title="Rebuild the image" onClose={() => onOpenChange(false)} />
      <CreateSurfaceBody className="space-y-3">
        <div role="radiogroup" aria-label="How to build" className="space-y-2">
          {options.map((o) => (
            <button
              key={o.id}
              type="button"
              role="radio"
              aria-checked={mode === o.id}
              onClick={() => setMode(o.id)}
              className={cn("block w-full rounded-md border p-3 text-left coarse:min-h-[44px]", mode === o.id ? "border-primary bg-primary/10" : "border-border hover:bg-muted")}
            >
              <b className="block text-body font-medium text-foreground">{o.title}</b>
              <span className="text-label text-muted-foreground">{o.hint}</span>
            </button>
          ))}
        </div>
        <p className="text-label text-muted-foreground">Agents keep the old image until the new one is ready.</p>
      </CreateSurfaceBody>
      <CreateSurfaceFooter onCancel={() => onOpenChange(false)} guardCancel={false} primaryLabel="Build" busy={busy} onPrimary={() => void build()} />
    </CreateSurface>
  )
}

function BuildHistory({ workspaceId, crewId }: { workspaceId: string; crewId: string }) {
  const [open, setOpen] = useState(false)
  const [rows, setRows] = useState<Revision[] | null>(null)
  const [failed, setFailed] = useState(false)
  useEffect(() => {
    if (!open || rows) return
    let cancelled = false
    apiFetch(`/api/v1/crews/${crewId}/provision/revisions?workspace_id=${encodeURIComponent(workspaceId)}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: { revisions?: Revision[] }) => { if (!cancelled) setRows(Array.isArray(v?.revisions) ? v.revisions : []) })
      .catch(() => { if (!cancelled) setFailed(true) })
    return () => { cancelled = true }
  }, [open, rows, crewId, workspaceId])
  return (
    <details className="border-t border-border px-4 py-2.5 text-label" onToggle={(e) => setOpen(e.currentTarget.open)}>
      <summary className="cursor-pointer text-muted-foreground hover:text-foreground">Build history</summary>
      {failed ? <p className="mt-2 text-chip-danger-fg">Build history could not be loaded.</p>
        : !rows ? (open ? <p className="mt-2 text-muted-foreground">Loading…</p> : null)
          : rows.length === 0 ? <p className="mt-2 text-muted-foreground">No builds recorded yet.</p>
            : (
              <ul className="mt-2 space-y-1">
                {rows.slice(0, 10).map((r, i) => (
                  <li key={r.id} className="flex flex-wrap gap-x-2">
                    <span className="font-mono">{(r.image_id || r.build_hash).replace(/^sha256:/, "").slice(0, 12)}</span>
                    <span className="text-muted-foreground">{new Date(r.created_at).toLocaleString()}{i === 0 ? " · latest" : ""}</span>
                  </li>
                ))}
              </ul>
            )}
    </details>
  )
}

function PreviewLinks({ workspaceId, crewId, canRevoke }: { workspaceId: string; crewId: string; canRevoke: boolean }) {
  const ws = encodeURIComponent(workspaceId)
  const [rows, setRows] = useState<PortExposure[] | null>(null)
  const [failed, setFailed] = useState(false)
  const [rev, setRev] = useState(0)
  useEffect(() => {
    let cancelled = false
    setFailed(false)
    apiFetch(`/api/v1/crews/${crewId}/port-expose?workspace_id=${ws}`)
      .then((r) => (r.ok ? r.json() : Promise.reject(r.status)))
      .then((v: PortExposure[]) => { if (!cancelled) setRows(Array.isArray(v) ? v : []) })
      .catch(() => { if (!cancelled) { setRows([]); setFailed(true) } })
    return () => { cancelled = true }
  }, [crewId, ws, rev])

  const revoke = async (p: PortExposure) => {
    const r = await apiFetch(`/api/v1/crews/${crewId}/port-expose/${encodeURIComponent(p.id)}/revoke?workspace_id=${ws}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ reason: "revoked from the crew Environment tab" }),
    })
    if (!r.ok) {
      toast.error("The link was not revoked", { description: `HTTP ${r.status}` })
      return
    }
    toast.success(`Port ${p.container_port} link revoked`)
    setRev((n) => n + 1)
  }

  return (
    <CrCard title="Preview links" count={rows?.length || undefined} concept="crews" data-testid="env-preview-links">
      {failed ? <CrNote tone="error">Preview links could not be loaded.</CrNote>
        : rows === null ? <CrNote>Loading…</CrNote>
          : rows.length === 0 ? <CrNote>None open. An agent opens one when it needs to show you a running app; it expires on its own.</CrNote>
            : rows.map((p) => (
              <CrLine
                key={p.id}
                title={`Port ${p.container_port}${p.description ? ` · ${p.description}` : ""}`}
                sub={`Opened by ${p.agent_slug} · expires ${new Date(p.expires_at).toLocaleString()}`}
                right={canRevoke ? <Button variant="ghost" size="sm" onClick={() => void revoke(p)}>Revoke</Button> : undefined}
              />
            ))}
    </CrCard>
  )
}
