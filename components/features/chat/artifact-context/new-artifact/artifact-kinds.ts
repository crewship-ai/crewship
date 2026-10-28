import { AppWindow, FileText, LayoutDashboard, PanelsTopLeft, Presentation, Sheet, type LucideIcon } from "lucide-react"
import type { PageView } from "@/hooks/use-pages"
import { ACCENT, type Accent, type AccentName } from "@/lib/concept-accents"

/**
 * The one registry of artifact kinds. The library, its New artifact dialog and
 * the chat "+" menu all read it, so a new kind is one entry here and no UI
 * change. Keys match the library's kinds and the server's `artifact_kind`.
 */
export type ArtifactKindKey = "project" | "dashboard" | "page" | "word" | "sheet" | "slides"

/** Web kinds render as screens or panels; document kinds are files with an editor. */
export type ArtifactFamily = "web" | "document"

/**
 * How a new one comes to exist: an agent writes v1 (`agent`), a person opens
 * an empty Craft holding the lock (`edit`), or an empty Page waits for crews
 * to add panels (`panels`).
 */
export type ArtifactMake = "agent" | "edit" | "panels"

/** What the current workspace and build can actually serve. Unknown is false. */
export interface ArtifactCapabilities {
  /** The server reports artifact projects available for this workspace. */
  projects: boolean
  /** The live Dashboard build flag. */
  dashboards: boolean
  /** Some document creation format is confirmed (Doc, WordCraft or commands). */
  documents: boolean
  /** GridCraft workbook creation is switched on for the workspace. */
  sheets: boolean
  /** The caller may create Pages at all (role or page.create). */
  pages: boolean
}

export const NO_CAPABILITIES: ArtifactCapabilities = { projects: false, dashboards: false, documents: false, sheets: false, pages: false }

export interface ArtifactKindEntry {
  key: ArtifactKindKey
  family: ArtifactFamily
  label: string
  /** The label on a New artifact thumbnail when the short one is ambiguous. */
  newLabel?: string
  plural: string
  description: string
  icon: LucideIcon
  /** The kind's colour from the tested concept palette: Word blue, Sheet green, Project purple, Dashboard teal, Slides orange, Page slate. */
  accent: AccentName
  /** What opens it, in the reader's words. */
  viewer: string
  make: ArtifactMake
  /** Placeholder for the Name field. */
  placeholder: string
  templates: string[]
  /** Accepted upload extensions; empty for kinds that are not files. */
  uploadExtensions: string[]
  /** An agent can write v1 from a chat prompt ("Describe it"). */
  drafted: boolean
  /** Starters for the chat "+" menu; the person edits them before sending. */
  starterPrompts: string[]
  /** Creatable now. False for a planned kind whatever the capabilities say. */
  live: (caps: ArtifactCapabilities) => boolean
}

export const ARTIFACT_KINDS: readonly ArtifactKindEntry[] = [
  {
    key: "project", drafted: true, family: "web", accent: "purple", label: "Project", plural: "Projects", icon: AppWindow,
    description: "Screens that link together", viewer: "Screens · Select · Compare", make: "agent",
    placeholder: "Pricing site", templates: ["Pricing site", "Sign-up flow", "Landing page", "Status page"], uploadExtensions: [],
    starterPrompts: ["A three-screen pricing site: plans, compare, FAQ", "A sign-up flow with a confirmation screen"],
    // Only an agent makes a project, through the explicit project-creation intent.
    live: caps => caps.projects,
  },
  {
    key: "dashboard", drafted: true, family: "web", accent: "teal", label: "Dashboard", plural: "Dashboards", icon: LayoutDashboard,
    description: "Numbers and tables agents keep current", viewer: "Live data with sources", make: "agent",
    placeholder: "Sales pipeline", templates: ["Sales pipeline", "Support SLA", "Cash flow", "Release health"], uploadExtensions: [],
    starterPrompts: ["Sales pipeline by stage with owners", "Support SLA for this week"],
    live: caps => caps.projects && caps.dashboards,
  },
  {
    key: "page", drafted: false, family: "web", accent: "slate", label: "Page", newLabel: "Page with panels", plural: "Pages", icon: PanelsTopLeft,
    description: "Monitors and queues crews keep filled", viewer: "Panels (monitors, queues)", make: "panels",
    placeholder: "Overdue invoices", templates: ["Monitor", "Approval queue", "Team board"], uploadExtensions: [],
    starterPrompts: [],
    live: caps => caps.pages,
  },
  {
    key: "word", drafted: true, family: "document", accent: "blue", label: "Word", newLabel: "Word document", plural: "Word", icon: FileText,
    description: "A document, one editor at a time", viewer: "Read-only preview · Edit in WordCraft", make: "edit",
    placeholder: "Partner brief", templates: ["Proposal", "Contract summary", "Meeting minutes", "Playbook"], uploadExtensions: [".docx"],
    starterPrompts: ["A partner brief from this chat", "Turn the weekly report into a two-page memo"],
    live: caps => caps.pages && caps.documents,
  },
  {
    key: "sheet", drafted: false, family: "document", accent: "green", label: "Sheet", plural: "Sheets", icon: Sheet,
    description: "Workbook with formulas in GridCraft", viewer: "Read-only preview · Edit in GridCraft", make: "edit",
    placeholder: "Price model", templates: ["Quote", "Budget", "Invoice batch", "Hiring plan"], uploadExtensions: [".xlsx", ".csv"],
    starterPrompts: ["Price model with seats and MRR", "Hiring plan for 2027"],
    live: caps => caps.pages && caps.sheets,
  },
  {
    key: "slides", drafted: false, family: "document", accent: "orange", label: "Slides", plural: "Slides", icon: Presentation,
    description: "A deck you can present", viewer: "Slides · Present · PDF", make: "agent",
    placeholder: "Quarterly review", templates: [], uploadExtensions: [],
    starterPrompts: [],
    // Listed, never creatable until it passes live acceptance (no PDF through MCP yet).
    live: () => false,
  },
]

const BY_KEY = new Map(ARTIFACT_KINDS.map(k => [k.key, k]))

export function artifactKind(key: ArtifactKindKey): ArtifactKindEntry {
  return BY_KEY.get(key)!
}

export const KIND_ICON: Record<ArtifactKindKey, LucideIcon> = Object.fromEntries(ARTIFACT_KINDS.map(k => [k.key, k.icon])) as Record<ArtifactKindKey, LucideIcon>

export const KIND_ACCENT: Record<ArtifactKindKey, Accent> = Object.fromEntries(ARTIFACT_KINDS.map(k => [k.key, ACCENT[k.accent]])) as Record<ArtifactKindKey, Accent>

export function creatableKinds(caps: ArtifactCapabilities): ArtifactKindEntry[] {
  return ARTIFACT_KINDS.filter(k => k.live(caps))
}

/** Listed in a "Coming" line rather than offered. */
export function comingKinds(caps: ArtifactCapabilities): ArtifactKindEntry[] {
  return ARTIFACT_KINDS.filter(k => !k.live(caps))
}

/** The kind of an existing Page: the version head first, then the storage it reports; a plain Page is a Page. */
export function artifactKindOfPage(page: PageView): ArtifactKindKey {
  if (page.artifactKind) return page.artifactKind
  if (page.hasWorkbook) return "sheet"
  if (page.hasSlides) return "slides"
  if (page.hasDocument) return "word"
  return "page"
}
