"use client"

/**
 * Every door in the product, as something you can open.
 *
 * The first cut of /design showed four abstract archetypes, which is the right
 * analysis and the wrong thing to review — "Compose, md, 640" tells you
 * nothing about whether `New agent` still has its tool profile. So the list is
 * the actual doors, named the way the product names them, each opening the
 * real field set behind it.
 *
 * `fields` is the completeness contract. It lists what the CURRENT surface
 * carries, read out of its component, and the surface here renders all of
 * them. A migration that ships with a shorter list has dropped a setting, and
 * this is where that shows up.
 */

import * as React from "react"

import {
  CreateSurface,
  CreateSurfaceFrame,
  type CreateSurfaceSize,
} from "@/components/layout/create-surface"
import { NewIssueContent, NewProjectContent } from "./issues"
import { NewRoutineContent, ImportRoutineContent } from "./routines"
import { NewPageContent, ImportPageContent } from "./pages"
import { NewCrewContent, NewAgentContent } from "./crews"
import {
  AddIntegrationContent,
  AddSecretContent,
  ConnectOAuthContent,
  ImportSkillContent,
} from "./library"

export type Archetype = "Compose" | "Pick" | "Wizard" | "Import"

export interface Door {
  id: string
  /** The page this button lives on. */
  page: string
  /** The label on the button, verbatim. */
  action: string
  /** CONCEPT_ICON key — supplies the glyph and the colour. */
  concept: string
  size: CreateSurfaceSize
  archetype: Archetype
  /** One line: what this door is for. */
  blurb: string
  /** Every setting the surface carries. The migration may not shorten this. */
  fields: string[]
  Content: React.ComponentType<{ onClose: () => void }>
}

export const DOORS: Door[] = [
  {
    id: "new-issue",
    page: "Issues",
    action: "New issue",
    concept: "issues",
    size: "md",
    archetype: "Compose",
    blurb: "The reference — now carrying the four fields POST /issues accepts and the modal never offered.",
    fields: [
      "crew", "title", "description", "status", "priority", "assignee", "project", "routine", "labels",
      "due date", "estimate", "milestone", "parent issue", "create more",
    ],
    Content: NewIssueContent,
  },
  {
    id: "new-project",
    page: "Issues",
    action: "New project",
    concept: "issues",
    size: "md",
    archetype: "Compose",
    blurb: "Same shape as New issue, one size up. Summary, labels and milestones are gone — the server binds none of them.",
    fields: ["name", "description", "icon", "colour", "status", "priority", "lead", "start date", "target date"],
    Content: NewProjectContent,
  },
  {
    id: "new-routine",
    page: "Routines",
    action: "New routine",
    concept: "routines",
    size: "lg",
    archetype: "Pick",
    blurb: "Three doors in one. Today they are three widths; here they are three screens at one width.",
    fields: ["mode", "goal", "drafting agent", "fork source", "name", "slug", "owner crew", "description", "DSL format", "starter template", "definition", "dry-run gate"],
    Content: NewRoutineContent,
  },
  {
    id: "import-routine",
    page: "Routines",
    action: "Import",
    concept: "routines",
    size: "sm",
    archetype: "Import",
    blurb: "Paste a bundle. The collision rule is a control now, not a paragraph.",
    fields: ["bundle JSON", "replace on collision"],
    Content: ImportRoutineContent,
  },
  {
    id: "new-page",
    page: "Pages",
    action: "New page",
    concept: "pages",
    size: "xl",
    archetype: "Compose",
    blurb: "A YAML document editor, not a form — the specimen said otherwise and was wrong. Loses the backdrop blur and gains a focus trap.",
    fields: ["the page document (apiVersion, kind, metadata, spec.panels)"],
    Content: NewPageContent,
  },
  {
    id: "import-page",
    page: "Pages",
    action: "Import",
    concept: "pages",
    size: "sm",
    archetype: "Import",
    blurb: "Keeps the best idea in the old surfaces: the refusal is rendered as a form to fill.",
    fields: ["bundle file", "install slug", "reference bindings"],
    Content: ImportPageContent,
  },
  {
    id: "new-crew",
    page: "Crews",
    action: "New crew",
    concept: "crews",
    size: "lg",
    archetype: "Wizard",
    blurb: "Five steps at ONE width. Today it grows 680 → 940px between step 1 and step 2.",
    fields: ["name", "slug", "description", "lineup", "CPU", "memory", "network policy", "base image", "devcontainer features", "MCP servers"],
    Content: NewCrewContent,
  },
  {
    id: "new-agent",
    page: "Crews",
    action: "New agent",
    concept: "crews",
    size: "lg",
    archetype: "Compose",
    blurb: "The product's largest form. Every field a person sets on AgentDraft is here; Advanced now says what is inside it.",
    fields: [
      "template", "avatar", "name", "slug", "crew", "role", "role title", "description",
      "system prompt", "provider", "model", "memory",
      "tool profile", "CLI adapter", "run timeout", "lead mode",
    ],
    Content: NewAgentContent,
  },
  {
    id: "import-skill",
    page: "Skills",
    action: "Import",
    concept: "skills",
    size: "md",
    archetype: "Import",
    blurb: "Three sources behind one door. The licence gate moves out of a stray checkbox.",
    fields: ["source", "URL", "pasted SKILL.md", "repository", "ref", "vendor", "dry run", "unrecognised licences"],
    Content: ImportSkillContent,
  },
  {
    id: "add-secret",
    page: "Credentials",
    action: "Add secret",
    concept: "credentials",
    size: "md",
    archetype: "Wizard",
    blurb: "The only door that already handled a phone. Its three steps survive unchanged, and the six shapes now come from CREDENTIAL_ITEM_TYPES rather than being invented.",
    fields: [
      "shape", "name", "secret", "shape-dependent fields", "account", "expiry",
      "security tier", "tags", "variable name", "scope", "crews",
    ],
    Content: AddSecretContent,
  },
  {
    id: "connect-oauth",
    page: "Credentials",
    action: "Connect via OAuth",
    concept: "credentials",
    size: "sm",
    archetype: "Pick",
    blurb: "Was a bare 448px form with the default dialog padding. Now the same tiles as every other picker.",
    fields: ["provider", "scope"],
    Content: ConnectOAuthContent,
  },
  {
    id: "add-integration",
    page: "Integrations",
    action: "Add integration",
    concept: "integrations",
    size: "lg",
    archetype: "Pick",
    blurb: "Kind, then service — the right shape already. It gains a footer and delivery defaults.",
    fields: ["kind", "service", "search", "retries", "quiet hours"],
    Content: AddIntegrationContent,
  },
]

/** Doors grouped by the page whose sub-bar carries them. */
export const DOORS_BY_PAGE: { page: string; concept: string; doors: Door[] }[] = [
  { page: "Issues", concept: "issues", doors: DOORS.filter((d) => d.page === "Issues") },
  { page: "Routines", concept: "routines", doors: DOORS.filter((d) => d.page === "Routines") },
  { page: "Pages", concept: "pages", doors: DOORS.filter((d) => d.page === "Pages") },
  { page: "Activity", concept: "activity", doors: [] },
  { page: "Crews", concept: "crews", doors: DOORS.filter((d) => d.page === "Crews") },
  { page: "Skills", concept: "skills", doors: DOORS.filter((d) => d.page === "Skills") },
  { page: "Credentials", concept: "credentials", doors: DOORS.filter((d) => d.page === "Credentials") },
  { page: "Integrations", concept: "integrations", doors: DOORS.filter((d) => d.page === "Integrations") },
]

/**
 * Opens a door as the real modal — what a pointer device gets.
 *
 * `key={door.id}` on the content is deliberate: a surface reopened after a
 * close must not come back on step 3 with somebody else's pasted token still
 * in state. Remount-per-open is the rule the real Add secret already follows
 * and the one the others should adopt with it.
 */
export function DoorDialog({
  door,
  open,
  onClose,
}: {
  door: Door | null
  open: boolean
  onClose: () => void
}) {
  // Type something into a surface, then press Esc or the ×: the shell asks
  // before throwing it away. `dirty` is faked here — a real surface computes
  // it from its own form state — because the point of the specimen is the
  // interaction, not the bookkeeping.
  const [dirty, setDirty] = React.useState(false)
  React.useEffect(() => setDirty(false), [door?.id, open])

  if (!door) return null
  return (
    <CreateSurface
      open={open}
      onOpenChange={(o) => !o && onClose()}
      size={door.size}
      dirty={dirty}
      discardLabel={door.action.toLowerCase()}
      ariaLabel={`${door.page} — ${door.action}`}
      onSubmit={onClose}
    >
      {/* Any keystroke inside the surface counts as unsaved input. Crude, and
          exactly right for a specimen: it makes the guard reachable in one
          gesture instead of requiring you to know which field arms it. */}
      <div className="contents" onInput={() => setDirty(true)}>
        <door.Content key={door.id} onClose={onClose} />
      </div>
    </CreateSurface>
  )
}

/**
 * The same door as the phone gets, inside a handset.
 *
 * Not a mock-up: `CreateSurfaceFrame mobile` is the production shell with the
 * phone layout forced on, and the content is the identical component the
 * dialog above renders. The only thing the frame adds is a 390px box to put it
 * in, because a portalled dialog cannot be shown inside a page.
 */
export function DoorPhone({ door, onClose }: { door: Door | null; onClose: () => void }) {
  if (!door) return null
  return (
    <div className="mx-auto w-[390px] max-w-full">
      <div className="relative overflow-hidden rounded-[2.25rem] border-[10px] border-foreground/[0.14] bg-background shadow-2xl">
        {/* 390 × 780 is an iPhone 15 viewport rounded down — big enough to be
            representative, small enough to sit in a page next to the desktop
            version. */}
        <div className="relative flex h-[720px] flex-col">
          {/* The page the sheet is covering. It is dimmed, not blurred — that
              is the whole argument, and it has to be visible here. */}
          <div className="flex h-9 shrink-0 items-center justify-between px-4 pt-1 text-[11px] font-medium text-foreground/70">
            <span>9:41</span>
            <span className="flex items-center gap-1">
              <span className="h-1.5 w-1.5 rounded-full bg-success" />
              Crewship
            </span>
          </div>
          <div className="flex-1 space-y-2 px-4 py-2 opacity-40">
            <div className="h-3 w-2/3 rounded bg-foreground/10" />
            <div className="h-3 w-1/2 rounded bg-foreground/10" />
            <div className="h-16 rounded-lg bg-foreground/[0.06]" />
            <div className="h-16 rounded-lg bg-foreground/[0.06]" />
          </div>

          <div className="absolute inset-0 bg-black/50" aria-hidden />

          <div className="absolute inset-x-0 bottom-0 max-h-[92%]">
            <CreateSurfaceFrame mobile className="max-h-[680px]">
              <door.Content key={`${door.id}-phone`} onClose={onClose} />
            </CreateSurfaceFrame>
          </div>
        </div>
      </div>
    </div>
  )
}
