"use client"

import * as React from "react"
import { BookOpen, CornerUpLeft, Component, Download, Palette, Ruler, Search, Type } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { artifactDesignSystem } from "@/lib/feature-flags"
import { cn } from "@/lib/utils"
import { hasDesignSystemFile, parseDesignSystem, DESIGN_SYSTEM_FILE, type DesignSystem } from "@/components/features/artifacts/design-system/manifest"
import { useArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactSlot, ArtifactTool, useArtifactFrame } from "../../frame"
import { slidesFrameStatus } from "../slides/slides-status"
import { ProjectVersionHistory } from "../slides/slides-detail"
import type { KindFrameReport } from "./design-detail"

/** A project whose manifest carries design-system.json is a Design in Library mode (kinds.ts `kindOf`). */
export function isDesignLibrary(snapshot: Pick<ArtifactProjectSnapshot, "manifest" | "artifact_kind"> | undefined): boolean {
  return Boolean(snapshot && artifactDesignSystem() && !snapshot.artifact_kind && hasDesignSystemFile(snapshot.manifest.files))
}

type SectionId = "overview" | "colors" | "typography" | "spacing" | "radii" | "components"
const SECTIONS: { id: SectionId; label: string; icon: typeof Palette; count: (system: DesignSystem) => number | null }[] = [
  { id: "overview", label: "Overview", icon: BookOpen, count: () => null },
  { id: "colors", label: "Colors", icon: Palette, count: system => system.colors.length },
  { id: "typography", label: "Typography", icon: Type, count: system => system.typography.length },
  { id: "spacing", label: "Spacing", icon: Ruler, count: system => system.spacing.length },
  { id: "radii", label: "Corner radius", icon: CornerUpLeft, count: system => system.radii.length },
  { id: "components", label: "Components", icon: Component, count: system => system.components.length },
]
const SAMPLE = "Agents propose, people decide."

/**
 * The read-only Library view of a design system (Harbor 2 v87 `ds`): the
 * section navigator in the panel slot, and Colors, Typography, Spacing,
 * Corner radius and Components from the admitted design-system.json. The
 * library is a versioned artifact, so History is the artifact's versions.
 * Nothing here edits or publishes: there is no backend for library
 * publishing, pinning or "Used by", so those stay hidden.
 */
export function DesignLibraryBody({ workspaceId, slug, version, onFrame }: { workspaceId: string; slug: string; version?: number; onFrame: (report: KindFrameReport) => void }) {
  const [viewing, setViewing] = React.useState<number | undefined>(version)
  const snapshot = useArtifactProjectSnapshot(workspaceId, slug, viewing)
  const data = snapshot.data
  const status = data ? slidesFrameStatus({ ready: true, canWrite: data.can_write === true, shown: data.manifest.version, latest: data.latest_version, refreshFailed: Boolean(snapshot.error) }) : null
  const canEdit = Boolean(data && !snapshot.error && data.can_write === true)
  const key = JSON.stringify([status, canEdit])
  const report = React.useRef(onFrame); report.current = onFrame
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the key holds everything reported
  React.useEffect(() => { report.current({ canEdit, status, kind: { kind: "design", mode: "library", badge: "Library" } }) }, [key])
  const system = React.useMemo(() => data ? parseDesignSystem(data.manifest.files) : null, [data])
  if (!data) return snapshot.error
    ? <p role="alert" className="p-6 pt-16 text-sm text-muted-foreground">This library could not be loaded.</p>
    : <div aria-busy="true" aria-label="Loading library" className="grid gap-3 p-6 pt-16"><Skeleton className="h-40 w-full" /><Skeleton className="h-60 w-full" /></div>
  if (!system) return <p role="alert" className="p-6 pt-16 text-sm">This Design System file is invalid or uses an unsupported format.</p>
  const source = data.manifest.files.find(file => file.path === DESIGN_SYSTEM_FILE)?.content ?? ""
  return <>
    <LibraryView system={system} version={data.manifest.version} source={source} />
    <ProjectVersionHistory data={data} workspaceId={workspaceId} slug={slug} canWrite={canEdit} setViewing={setViewing} />
  </>
}

/** Renders the Library body for a design system; anything else renders `children` (the project view). */
export function DesignLibraryGate({ workspaceId, slug, version, onFrame, children }: { workspaceId: string; slug: string; version?: number; onFrame: (report: KindFrameReport) => void; children: React.ReactNode }) {
  // The same query the project viewer makes, so nothing is fetched twice; nothing at all with the switch off.
  const snapshot = useArtifactProjectSnapshot(workspaceId, artifactDesignSystem() ? slug : null, version)
  return isDesignLibrary(snapshot.data) ? <DesignLibraryBody workspaceId={workspaceId} slug={slug} version={version} onFrame={onFrame} /> : <>{children}</>
}

export function LibraryView({ system, version, source }: { system: DesignSystem; version: number; source: string }) {
  const frame = useArtifactFrame()
  const phone = frame?.layout === "phone"
  const [query, setQuery] = React.useState("")
  const [active, setActive] = React.useState<SectionId>("overview")
  const scroller = React.useRef<HTMLDivElement>(null)
  const needle = query.trim().toLowerCase()
  const match = (name: string) => !needle || name.toLowerCase().includes(needle)
  const colors = system.colors.filter(token => match(token.name) || token.value.includes(needle))
  const typography = system.typography.filter(style => match(style.name))
  const spacing = system.spacing.filter(token => match(token.name))
  const radii = system.radii.filter(token => match(token.name))
  const components = system.components.filter(component => match(component.name))
  const go = (id: SectionId) => {
    setActive(id)
    scroller.current?.querySelector(`[data-ds-section="${id}"]`)?.scrollIntoView?.({ block: "start", behavior: "smooth" })
  }
  const download = () => {
    const url = URL.createObjectURL(new Blob([source], { type: "application/json" }))
    try {
      const link = document.createElement("a")
      link.href = url; link.download = `design-system-v${version}.json`; link.click()
    } finally { setTimeout(() => URL.revokeObjectURL(url), 0) }
  }
  const head = (id: SectionId, title: string, count: number) => <header data-ds-section={id} className="mb-3 flex scroll-mt-16 items-baseline gap-2 border-b pb-2">
    <h2 className="text-lg font-semibold">{title}</h2><span className="text-xs text-muted-foreground">{count}</span>
  </header>
  const none = <p className="text-sm text-muted-foreground">{needle ? "Nothing matches the search." : "None defined."}</p>
  const navigator = <nav aria-label="Sections" className="grid gap-0.5 p-2">
    {SECTIONS.map(section => {
      const count = section.count(system)
      return <button key={section.id} type="button" aria-current={active === section.id ? "true" : undefined} onClick={() => go(section.id)}
        className="flex min-h-8 items-center gap-2 rounded-[var(--radius-row)] px-2.5 text-left text-sm text-muted-foreground hover:bg-accent hover:text-foreground aria-[current=true]:bg-primary/12 aria-[current=true]:text-primary-hover coarse:min-h-[44px]">
        <section.icon aria-hidden className="size-4 shrink-0" /><span className="flex-1 truncate">{section.label}</span>
        {count !== null && <span className="font-mono text-xs">{count}</span>}
      </button>
    })}
  </nav>
  return <div className="relative flex min-h-0 flex-1 flex-col bg-background">
    <div ref={scroller} className={cn("min-h-0 flex-1 overflow-auto pb-10", phone ? "px-3 pt-16" : "pl-[272px] pr-6 pt-16")}>
      <div className="mx-auto grid max-w-4xl gap-8">
        <section data-ds-section="overview" aria-label="Overview" className="grid scroll-mt-16 overflow-hidden rounded-card border sm:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
          <div aria-hidden className="relative flex min-h-32 items-end">
            {system.colors.slice(0, 4).map(color => <span key={color.name} className="h-full flex-1" style={{ backgroundColor: color.value }} />)}
            {system.colors.length === 0 && <span className="h-full flex-1 bg-muted" />}
            <span className="absolute bottom-2 left-3 text-5xl font-semibold text-white mix-blend-difference">Aa</span>
          </div>
          <div className="grid content-center gap-1.5 p-5">
            <small className="font-mono text-xs uppercase tracking-wide text-muted-foreground">Library · v{version}</small>
            <h1 className="text-2xl font-semibold">{system.name ?? "Design system"}</h1>
            <p className="text-sm text-muted-foreground">{system.colors.length} colors · {system.typography.length} text styles · {system.spacing.length} spacing steps · {system.radii.length} radii{system.components.length ? ` · ${system.components.length} components` : ""}</p>
          </div>
        </section>
        <section aria-label="Colors">
          {head("colors", "Colors", colors.length)}
          {colors.length === 0 ? none : <ul className="grid grid-cols-[repeat(auto-fill,minmax(150px,1fr))] gap-3">
            {colors.map(color => <li key={color.name} data-token={`color.${color.name}`} className="overflow-hidden rounded-card border">
              <div aria-hidden className="h-14" style={{ backgroundColor: color.value }} />
              <div className="grid px-2.5 py-1.5 text-xs"><b className="truncate font-medium">{color.name}</b><code className="text-muted-foreground">{color.value}</code></div>
            </li>)}
          </ul>}
        </section>
        <section aria-label="Typography">
          {head("typography", "Typography", typography.length)}
          {typography.length === 0 ? none : <ul className="divide-y rounded-card border">
            {typography.map(style => <li key={style.name} data-token={`typography.${style.name}`} className="grid gap-1 px-3 py-2.5 sm:grid-cols-[180px_minmax(0,1fr)] sm:items-baseline">
              <span className="text-xs text-muted-foreground"><b className="font-medium text-foreground">{style.name}</b> · {style.size}px{style.weight ? ` / ${style.weight}` : ""}{style.lineHeight ? ` / ${style.lineHeight}` : ""}{style.fontFamily ? ` · ${style.fontFamily}` : ""}</span>
              <span className="truncate" style={{ fontSize: style.size, fontWeight: style.weight ?? undefined, lineHeight: style.lineHeight ?? undefined, fontFamily: style.fontFamily ?? undefined }}>{SAMPLE}</span>
            </li>)}
          </ul>}
        </section>
        <section aria-label="Spacing">
          {head("spacing", "Spacing", spacing.length)}
          {spacing.length === 0 ? none : <ul className="grid gap-1.5">
            {spacing.map(space => <li key={space.name} data-token={`spacing.${space.name}`} className="flex items-center gap-3 text-xs">
              <span className="w-28 shrink-0 truncate font-medium">{space.name}</span><code className="w-14 shrink-0 text-muted-foreground">{space.value}px</code>
              <span aria-hidden className="h-3 max-w-full rounded-sm bg-primary/60" style={{ width: space.value }} />
            </li>)}
          </ul>}
        </section>
        <section aria-label="Corner radius">
          {head("radii", "Corner radius", radii.length)}
          {radii.length === 0 ? none : <ul className="flex flex-wrap gap-4">
            {radii.map(radius => <li key={radius.name} data-token={`radius.${radius.name}`} className="flex flex-col items-center gap-1 text-xs">
              <span aria-hidden className="size-14 border-2 border-primary/60 bg-muted" style={{ borderRadius: radius.value }} />
              <b className="font-medium">{radius.name}</b><code className="text-muted-foreground">{radius.value}px</code>
            </li>)}
          </ul>}
        </section>
        <section aria-label="Components">
          {head("components", "Components", components.length)}
          {components.length === 0 ? none : <ul className="divide-y rounded-card border text-sm">
            {components.map(component => <li key={component.name} className="px-3 py-2"><b className="font-medium">{component.name}</b>{component.description && <p className="text-xs text-muted-foreground">{component.description}</p>}</li>)}
          </ul>}
        </section>
      </div>
    </div>

    <ArtifactSlot name="view" label="View">
      <span className="inline-flex h-[30px] items-center gap-1.5 rounded-[var(--radius-row)] bg-primary/12 px-2 text-sm font-medium text-primary-hover"><BookOpen aria-hidden className="size-3.5" />Library</span>
      <label className="relative inline-flex items-center">
        <Search aria-hidden className="pointer-events-none absolute left-2 size-3.5 text-muted-foreground" />
        <input type="search" aria-label="Search the library" placeholder="Search the library" value={query} onChange={event => setQuery(event.target.value)}
          className="h-[30px] w-44 rounded-[var(--radius-row)] bg-muted/60 pl-7 pr-2 text-sm outline-none focus-visible:ring-2 focus-visible:ring-primary/40 coarse:min-h-[44px] md:w-56" />
      </label>
      {phone && SECTIONS.slice(1).map(section => <ArtifactTool key={section.id} icon={section.icon} label={section.label} on={active === section.id} onClick={() => go(section.id)} />)}
    </ArtifactSlot>
    <ArtifactSlot name="output" label="Output">
      <ArtifactTool icon={Download} label="Download design-system.json" onClick={download} />
    </ArtifactSlot>
    {!phone && <ArtifactSlot name="panel" label="Sections">{navigator}</ArtifactSlot>}
  </div>
}
