import { AppWindow, ChartColumn, FileSpreadsheet, FileText, NotebookText, PenTool, Presentation, type LucideIcon } from "lucide-react"
import { ACCENT, type Accent, type AccentName } from "@/lib/concept-accents"
import type { LibraryKind } from "./library-items"

/**
 * The one registry of what an artifact IS in the UI (Harbor 2 v87): five
 * Artifact kinds people and agents make together, and two Craft kinds that are
 * files with one editor at a time. Every label, icon, colour and thumbnail in
 * the Artifacts area reads it, so a kind is named the same everywhere.
 *
 * Storage kinds (`LibraryKind`, the server's `artifact_kind` and content
 * types) are not UI kinds: `kindOf` maps them here. The word "Page" is a
 * storage word and never a label.
 */
export type ArtifactKind = "docs" | "dashboard" | "slides" | "design" | "app" | "word" | "sheet"

/** Artifact: made and changed together, live or versioned. Craft: a .docx or .xlsx with one editor at a time. */
export type KindFamily = "Artifact" | "Craft"

export interface KindDef {
  id: ArtifactKind
  label: string
  plural: string
  icon: LucideIcon
  /** A token from the tested concept palette (globals.css), never a raw colour. */
  accent: AccentName
  /** One line, in the reader's words. */
  description: string
  /** What one of them is called in a sentence, lower case: "Ask about this document…". */
  noun: string
  family: KindFamily
}

export const KINDS: Record<ArtifactKind, KindDef> = {
  docs: { id: "docs", label: "Docs", plural: "Docs", icon: NotebookText, accent: "indigo", family: "Artifact", noun: "document", description: "Write together, live. Page or Canvas, with live embeds." },
  dashboard: { id: "dashboard", label: "Dashboard", plural: "Dashboards", icon: ChartColumn, accent: "blue", family: "Artifact", noun: "dashboard", description: "Declared data views that routines keep fresh." },
  slides: { id: "slides", label: "Slides", plural: "Slides", icon: Presentation, accent: "orange", family: "Artifact", noun: "deck", description: "A deck you present or export." },
  design: { id: "design", label: "Design", plural: "Designs", icon: PenTool, accent: "rose", family: "Artifact", noun: "design", description: "Artboards and libraries. A design system is a library." },
  app: { id: "app", label: "App", plural: "Apps", icon: AppWindow, accent: "purple", family: "Artifact", noun: "app", description: "HTML you can run: from a static site to a tool with capabilities." },
  word: { id: "word", label: "Word", plural: "Word", icon: FileText, accent: "sky", family: "Craft", noun: "document", description: "A .docx file with one editor at a time." },
  sheet: { id: "sheet", label: "Sheet", plural: "Sheets", icon: FileSpreadsheet, accent: "green", family: "Craft", noun: "sheet", description: "An .xlsx file with one editor at a time." },
}

/** Display order: the Artifact family first, then Craft. */
export const KIND_ORDER: readonly ArtifactKind[] = ["docs", "dashboard", "slides", "design", "app", "word", "sheet"]

export const kindDef = (kind: ArtifactKind): KindDef => KINDS[kind]
export const kindAccent = (kind: ArtifactKind): Accent => ACCENT[KINDS[kind].accent]

/** A view of one kind: Docs as a Page or a Canvas, Design as artboards or a Library. */
export type KindMode = "page" | "canvas" | "library"

/** A short qualifier shown next to the kind; never a second kind. */
export type KindBadge = "Static" | "Canvas" | "Library"

export interface ResolvedKind {
  kind: ArtifactKind
  mode?: KindMode
  badge?: KindBadge
}

/**
 * The server's content types (`internal/pages/content_types.go`
 * DeriveContentTypes). The catalog does not send them today; a caller that
 * has one (a Page detail, a content descriptor) passes it and wins over the
 * storage flags.
 */
export type ContentType = "panels" | "doc" | "board" | "slides" | "gridcraft-workbook" | "html" | "application" | "mooring-app"

/** What `kindOf` reads: a LibraryItem satisfies it, and so does anything that knows its storage. */
export interface KindSource {
  kind: LibraryKind
  /** A Word or Sheet with a version head (native Craft). A Word without one is a legacy doc. */
  native?: boolean
  /** A published application with capabilities exists (`has_application`). */
  hasApplication?: boolean
  contentType?: ContentType
  /** The selected manifest carries `design-system.json` (the catalog's `artifact_profile`, or the project once read). */
  designSystem?: boolean
  /** The selected manifest carries `interactive.json`: an App running its own scripts, whatever data it declares. */
  interactive?: boolean
}

/**
 * Storage → UI kind. Pure and total: every LibraryKind and content type lands
 * on exactly one kind, so nothing in the library is ever unnamed.
 *
 * - A design system (a manifest with design-system.json) is a Design in Library mode.
 * - An interactive project (interactive.json) is an App, never Static and never a Dashboard:
 *   a helper data collection does not make a running app a data view.
 * - A project is an App; without a published application it is Static.
 * - A legacy Page that publishes an application is an App; without one it is its data panels, a Dashboard.
 * - A board is Docs in Canvas mode; a legacy doc (Word storage without a version head) is Docs.
 * - Native Word and Sheet stay Craft.
 */
export function kindOf(source: KindSource): ResolvedKind {
  if (source.designSystem) return { kind: "design", mode: "library", badge: "Library" }
  if (source.interactive && (source.kind === "project" || source.kind === "dashboard" || source.kind === "page")) return { kind: "app" }
  switch (source.contentType) {
    case "board": return { kind: "docs", mode: "canvas", badge: "Canvas" }
    case "doc": return source.kind === "word" && source.native ? { kind: "word" } : { kind: "docs", mode: "page" }
    case "slides": return { kind: "slides" }
    case "gridcraft-workbook": return { kind: "sheet" }
    case "html": return { kind: "app", badge: "Static" }
    case "mooring-app": case "application": return { kind: "app" }
    case "panels": return source.hasApplication ? { kind: "app" } : { kind: "dashboard" }
    default: break
  }
  switch (source.kind) {
    case "word": return source.native ? { kind: "word" } : { kind: "docs", mode: "page" }
    case "sheet": return { kind: "sheet" }
    case "slides": return { kind: "slides" }
    case "dashboard": return { kind: "dashboard" }
    case "design": return { kind: "design", mode: "canvas" }
    case "project":
      return source.hasApplication ? { kind: "app" } : { kind: "app", badge: "Static" }
    case "page":
      return source.hasApplication ? { kind: "app" } : { kind: "dashboard" }
  }
}

/** The kind's label with its qualifier, e.g. "App · Static". */
export function kindText(resolved: ResolvedKind): string {
  return resolved.badge ? `${KINDS[resolved.kind].label} · ${resolved.badge}` : KINDS[resolved.kind].label
}

/** How many items of each UI kind, in registry order; kinds with none are left out. */
export function uiKindCounts(items: readonly KindSource[]): [ArtifactKind, number][] {
  const counts = new Map<ArtifactKind, number>()
  for (const item of items) {
    const kind = kindOf(item).kind
    counts.set(kind, (counts.get(kind) ?? 0) + 1)
  }
  return KIND_ORDER.filter(kind => counts.has(kind)).map(kind => [kind, counts.get(kind)!])
}
