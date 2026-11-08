"use client"

import * as React from "react"
import Link from "next/link"
import { BookOpen, Code2, CornerUpLeft, Component, Download, Palette, Ruler, Search, Type } from "lucide-react"
import { Skeleton } from "@/components/ui/skeleton"
import { MarkdownContent } from "@/components/features/issues/markdown-content"
import { artifactDesignSystem } from "@/lib/feature-flags"
import { cn } from "@/lib/utils"
import { hasDesignSystemFile, parseDesignSystem, DESIGN_SYSTEM_FILE, type DesignSystem } from "@/components/features/artifacts/design-system/manifest"
import { useArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/use-artifact-project-data"
import type { ArtifactProjectSnapshot } from "@/components/features/chat/artifact-context/projects/types"
import { ArtifactViewerProvider, type ArtifactViewerTarget } from "@/components/features/chat/artifact-context/projects/artifact-viewer-context"
import { ArtifactViewerPanel, type ArtifactViewerParts } from "@/components/features/chat/artifact-context/viewer/artifact-viewer-panel"
import { ArtifactSlot, ArtifactTool, ArtifactToolSeparator, useArtifactFrame } from "../../frame"
import { slidesFrameStatus } from "../slides/slides-status"
import { ProjectDrawer, VersionMenu } from "../project-parts"
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
/** The library's brand book, next to design-system.json in the manifest. */
export const LIBRARY_README_FILE = "README.md"
const PREVIEW_HEIGHT = { fallback: 240, min: 80, max: 1200 }
/** A component card is laid out on a 960px canvas (the claude.ai Design System card) and scaled down to fit. */
const PREVIEW_WIDTH = 960
// The preview is the system's own HTML: an opaque origin with no scripts and no network, like the chat artifact pane.
const PREVIEW_CSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; font-src data:; form-action 'none'; base-uri 'none'">`

/** What the Library shows beside the tokens: the README brand book and each component's `components/<Name>/preview.html`. */
export interface LibraryExtras { readme: string | null; previews: Record<string, string> }

export function libraryExtras(system: DesignSystem, files: readonly { path: string; content: string }[]): LibraryExtras {
  const byPath = new Map(files.map(file => [file.path, file.content]))
  const previews: Record<string, string> = {}
  for (const component of system.components) {
    const html = byPath.get(`components/${component.name}/preview.html`)
    if (html?.trim()) previews[component.name] = html
  }
  const readme = byPath.get(LIBRARY_README_FILE)
  return { readme: readme?.trim() ? readme : null, previews }
}

/** The README without a leading `# <name>` heading: the overview above it already names the system. */
export function readmeBody(readme: string, name: string | null | undefined): string {
  const lead = /^\s*#[ \t]+(.+?)[ \t#]*(?:\r?\n|$)/.exec(readme)
  return lead && name && lead[1].trim().toLowerCase() === name.trim().toLowerCase() ? readme.slice(lead[0].length).replace(/^\s+/, "") : readme
}

/**
 * A component preview as its system renders it: the colour tokens become CSS
 * custom properties (`var(--name)`). Names and values are the admitted
 * literals (lowercase identifiers, #rrggbb), so they cannot leave the rule.
 */
export function previewDocument(system: DesignSystem, html: string): string {
  const vars = system.colors.map(color => `--${color.name}:${color.value}`).join(";")
  return `${PREVIEW_CSP}<style>:root{${vars}}</style>${html}`
}

/** The preview's own `<!-- @dsCard height=N -->` hint, bounded; otherwise a default. */
export function previewHeight(html: string): number {
  const hint = /<!--\s*@dsCard\b[^>]*?\bheight=(\d{1,4})\b/.exec(html)
  const height = hint ? Number(hint[1]) : PREVIEW_HEIGHT.fallback
  return Math.min(PREVIEW_HEIGHT.max, Math.max(PREVIEW_HEIGHT.min, height))
}

/**
 * The Library of a design system (Harbor 2 v87 `ds`), laid out in the frame
 * from the project viewer's parts: the frame owns the title, status and
 * share; the body is the library itself (README, colours, typography,
 * spacing, radii, components) with the section navigator in the panel slot.
 * Version, Share and Edit source sit in the view and output slots; the
 * comment queue, version history and Propose go to the drawer
 * (`ProjectDrawer`). Every rule and backend call stays in the viewer.
 * There is no backend for library publishing, pinning, "Used by" or a
 * Canvas mode of a library, so those stay hidden.
 */
export function DesignLibraryProjectView({ parts, onFrame }: { parts: ArtifactViewerParts; onFrame: (report: KindFrameReport) => void }) {
  const { data, version } = parts
  const status = slidesFrameStatus({ ready: true, canWrite: parts.canWrite, shown: version.shown, latest: version.latest, refreshFailed: false })
  const key = JSON.stringify([status, parts.canWrite])
  const report = React.useRef(onFrame); report.current = onFrame
  // eslint-disable-next-line react-hooks/exhaustive-deps -- the key holds everything reported
  React.useEffect(() => { report.current({ canEdit: parts.canWrite, status, kind: LIBRARY_KIND }) }, [key])
  const system = React.useMemo(() => parseDesignSystem(data.manifest.files), [data])
  const notices = <div className="grid gap-2 px-3 pt-16 empty:hidden md:px-6">{parts.banners}</div>
  if (!system) return <>{notices}<p role="alert" className="p-6 pt-16 text-sm">This Design System file is invalid or uses an unsupported format.</p><ProjectDrawer parts={parts} /></>
  const source = data.manifest.files.find(file => file.path === DESIGN_SYSTEM_FILE)?.content ?? ""
  return <>
    <LibraryView system={system} version={version.shown} source={source} extras={libraryExtras(system, data.manifest.files)} notices={parts.banners}
      view={<><ArtifactToolSeparator /><VersionMenu parts={parts} />{parts.share}</>}
      output={parts.editSourceHref && <Link href={parts.editSourceHref} aria-label="Edit source" title="Edit source"
        className="inline-flex h-[30px] min-w-[30px] items-center justify-center rounded-[var(--radius-row)] px-[7px] text-muted-foreground hover:bg-accent hover:text-foreground coarse:min-h-[44px] coarse:min-w-[44px]"><Code2 aria-hidden className="size-4" /></Link>} />
    <ProjectDrawer parts={parts} />
  </>
}

const LIBRARY_KIND = { kind: "design", mode: "library", badge: "Library" } as const

/**
 * A project whose manifest is a design system opens as its Library; any other
 * project renders `children` (its App, Dashboard or viewer). The catalog cannot
 * tell a design system from an App before the project is read, so the gate
 * comes first and holds the stage while that read is under way.
 */
export function DesignLibraryGate({ workspaceId, target, onBack, onFrame, children }: { workspaceId: string; target: ArtifactViewerTarget; onBack: () => void; onFrame: (report: KindFrameReport) => void; children: React.ReactNode }) {
  // The same query the project viewer makes, so nothing is fetched twice; nothing at all with the switch off.
  const snapshot = useArtifactProjectSnapshot(workspaceId, artifactDesignSystem() ? target.slug : null, target.version)
  const frame = useArtifactFrame()
  const openDrawer = frame?.openDrawer
  const layout = frame?.layout
  const onSection = React.useCallback((section: "history" | "comments" | null) => {
    if (section) openDrawer?.(section)
    else if (layout !== "desktop") openDrawer?.(null)
  }, [openDrawer, layout])
  if (snapshot.isLoading && !snapshot.data) return <div aria-busy="true" aria-label="Loading artifact" className="grid gap-3 p-6 pt-16"><Skeleton className="h-40 w-full" /><Skeleton className="h-60 w-full" /></div>
  if (!isDesignLibrary(snapshot.data)) return <>{children}</>
  const render = (parts: ArtifactViewerParts) => parts.previewKind === "design-system" ? <DesignLibraryProjectView parts={parts} onFrame={onFrame} /> : undefined
  return <ArtifactViewerProvider workspaceId={workspaceId} target={target} onTargetChange={next => { if (!next) onBack() }}>
    <div className="flex min-h-[520px] flex-1 overflow-hidden"><ArtifactViewerPanel variant="library" render={render} onSection={onSection} /></div>
  </ArtifactViewerProvider>
}

export function LibraryView({ system, version, source, extras = { readme: null, previews: {} }, notices, view, output }: {
  system: DesignSystem; version: number; source: string; extras?: LibraryExtras
  /** The viewer's conflict, retry and version notices, above the library. */
  notices?: React.ReactNode
  /** Version and Share after the Library switch and search. */
  view?: React.ReactNode
  /** Edit source beside Download. */
  output?: React.ReactNode
}) {
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
        {notices && <div className="grid gap-2 empty:hidden">{notices}</div>}
        <section data-ds-section="overview" aria-label="Overview" className="grid scroll-mt-16 overflow-hidden rounded-card border sm:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]">
          <div aria-hidden className="relative flex min-h-32 items-end">
            {system.colors.slice(0, 4).map(color => <span key={color.name} className="h-full flex-1" style={{ backgroundColor: color.value }} />)}
            {system.colors.length === 0 && <span className="h-full flex-1 bg-muted" />}
            <span className="absolute bottom-2 left-3 text-5xl font-semibold text-white mix-blend-difference">Aa</span>
          </div>
          <div className="grid content-center gap-1.5 p-5">
            <small className="text-xs text-muted-foreground">Library · v{version}</small>
            <h1 className="text-2xl font-semibold">{system.name ?? "Design system"}</h1>
            <p className="text-sm text-muted-foreground">{system.colors.length} colors · {system.typography.length} text styles · {system.spacing.length} spacing steps · {system.radii.length} radii{system.components.length ? ` · ${system.components.length} ${system.components.length === 1 ? "component" : "components"}` : ""}</p>
          </div>
        </section>
        {extras.readme && <article aria-label="README" className="rounded-card border px-5 py-4">
          <MarkdownContent>{readmeBody(extras.readme, system.name)}</MarkdownContent>
        </article>}
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
            {components.map(component => {
              const preview = extras.previews[component.name]
              return <li key={component.name} data-component={component.name} className="grid gap-2 px-3 py-2">
                <div><b className="font-medium">{component.name}</b>{component.description && <p className="text-xs text-muted-foreground">{component.description}</p>}</div>
                {preview && <ComponentPreview name={component.name} document={previewDocument(system, preview)} height={previewHeight(preview)} />}
              </li>
            })}
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
      {view}
      {phone && SECTIONS.slice(1).map(section => <ArtifactTool key={section.id} icon={section.icon} label={section.label} on={active === section.id} onClick={() => go(section.id)} />)}
    </ArtifactSlot>
    <ArtifactSlot name="output" label="Output">
      <ArtifactTool icon={Download} label="Download design-system.json" onClick={download} />
      {output}
    </ArtifactSlot>
    {!phone && <ArtifactSlot name="panel" label="Sections">{navigator}</ArtifactSlot>}
  </div>
}

/** The card at its canvas size, scaled to the column so a phone sees all of it rather than a crop. */
function ComponentPreview({ name, document, height }: { name: string; document: string; height: number }) {
  const box = React.useRef<HTMLDivElement>(null)
  const [scale, setScale] = React.useState(1)
  React.useEffect(() => {
    const node = box.current
    if (!node || typeof ResizeObserver === "undefined") return
    const observer = new ResizeObserver(([entry]) => setScale(Math.min(1, entry.contentRect.width / PREVIEW_WIDTH) || 1))
    observer.observe(node)
    return () => observer.disconnect()
  }, [])
  return <div ref={box} data-preview-scale={scale} className="overflow-hidden rounded-[var(--radius-row)] border bg-muted" style={{ height: height * scale }}>
    <iframe title={`${name} preview`} sandbox="" referrerPolicy="no-referrer" srcDoc={document}
      className="block origin-top-left border-0" style={{ width: PREVIEW_WIDTH, height, transform: scale < 1 ? `scale(${scale})` : undefined }} />
  </div>
}
