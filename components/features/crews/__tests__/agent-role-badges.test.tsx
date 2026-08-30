import { readFileSync } from "node:fs"
import path from "node:path"

import { describe, it, expect, vi, afterEach } from "vitest"
import { render, screen, cleanup } from "@testing-library/react"

import { CrewAgentPreviewList } from "../crew-agent-preview-list"
import { EmptyRoster } from "../empty-roster"
import { RosterTab } from "../crew-canvas-tabs/roster-tab"

// The avatars on two of these surfaces fire a background PUT; vitest.setup.ts
// fails any unmocked network call. Not what this file is about.
vi.mock("@/lib/agent-avatar-persist", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/agent-avatar-persist")>()),
  queueAvatarBackfill: vi.fn(),
}))

/**
 * A role badge is a promise: it names something the product can create. The
 * create-crew wizard's lineup could render `COORDINATOR` — retired in v0.1,
 * refused by POST /api/v1/agents with 400 "agent_role must be AGENT or LEAD" —
 * because both renderers printed whatever token arrived (#2197).
 *
 * Following #2192, the guard is over a *vocabulary* rather than the one
 * literal already known to be wrong: no renderer may put a role the create
 * endpoint does not accept on screen, whichever token the server or a
 * language model produces.
 */

/** What POST /api/v1/agents accepts — internal/api/agents.go validAgentRoles. */
const SUPPORTED_ROLE_NAMES = ["AGENT", "LEAD"] as const

/**
 * Tokens no create path accepts: COORDINATOR is the retired one that produced
 * this bug (#2166, #2189, #2195 are its other surfaces); the rest are the
 * near-misses a model is likely to invent for a slot described as "this one
 * coordinates the crew's work". The defect is the class, not the token we
 * happened to see.
 */
const UNSUPPORTED_ROLE_NAMES = ["COORDINATOR", "ORCHESTRATOR", "SUPERVISOR", "MANAGER", "WORKER"] as const

/** Same token, the casings JSON from a model actually arrives in. */
function casings(role: string): string[] {
  const lower = role.toLowerCase()
  return [role, lower, lower[0].toUpperCase() + lower.slice(1)]
}

/**
 * Everything the rendered surface says, as separate words.
 *
 * Not `document.body.textContent`: that concatenates adjacent nodes with no
 * separator, so a badge next to a name reads "NovaCOORDINATORrt" and a
 * word-bounded search silently matches nothing — a guard that passes on the
 * unfixed code. Text nodes are joined with a space, and the two attributes
 * that also speak to the user (`title`, `aria-label`) are included, because a
 * tooltip can promise a role just as loudly as a badge.
 */
function renderedCopy(): string {
  const parts: string[] = []
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    parts.push(n.textContent ?? "")
  }
  for (const el of Array.from(document.body.querySelectorAll("[title],[aria-label]"))) {
    parts.push(el.getAttribute("title") ?? "", el.getAttribute("aria-label") ?? "")
  }
  return parts.join(" ")
}

/** Word-bounded and case-insensitive: "coordinator" is caught, "coordinated" is not. */
function namesRole(copy: string, role: string): boolean {
  return new RegExp(`\\b${role}\\b`, "i").test(copy)
}

function previewAgent(agent_role: string) {
  return {
    name: "Nova",
    slug: "nova",
    role_title: "Writes the docs",
    agent_role,
    system_prompt: "You write documentation.",
  }
}

function rosterAgent(agent_role: string) {
  return {
    id: "a1",
    name: "Nova",
    slug: "nova",
    status: "IDLE",
    role_title: "Writes the docs",
    agent_role,
    crew_id: "crew_ops",
  } as never
}

function crewRosterAgent(agent_role: string) {
  return {
    id: "a1",
    name: "Nova",
    slug: "nova",
    status: "IDLE",
    role_title: "Writes the docs",
    agent_role,
  } as never
}

const CREWS = [{ id: "crew_ops", slug: "ops", name: "Ops" }]

/** Only the fields RosterTab reads; the rest of CrewRecord is irrelevant here. */
const CREW_RECORD = { id: "crew_ops", slug: "ops", name: "Ops", avatar_style: null } as never

function renderPreview(role: string) {
  render(<CrewAgentPreviewList agents={[previewAgent(role)]} />)
}

function renderRoster(role: string) {
  render(<EmptyRoster agents={[rosterAgent(role)]} crews={CREWS} onAgentSelect={vi.fn()} />)
}

function renderCrewRoster(role: string) {
  render(
    <RosterTab crew={CREW_RECORD} agentsForCrew={[crewRosterAgent(role)]} members={null} onSelectAgent={vi.fn()} />,
  )
}

/**
 * Every surface that turns an `agent_role` into something a person reads.
 *
 * RosterTab is the one this guard was extended for: it carried the same
 * `a.agent_role !== "AGENT" && <span>{a.agent_role}</span>` as EmptyRoster, on
 * the same API data, and unlike CrewAgentPreviewList — which has no importers
 * at all — it is mounted today (crew-canvas.tsx). Covering a dead component
 * and missing its live twin is the failure mode this list exists to prevent:
 * a new renderer of `agent_role` belongs here on the day it is written.
 */
const SURFACES: { name: string; render: (role: string) => void }[] = [
  { name: "CrewAgentPreviewList (the wizard's lineup preview)", render: renderPreview },
  { name: "EmptyRoster (the agent roster)", render: renderRoster },
  { name: "RosterTab (the crew canvas roster)", render: renderCrewRoster },
]

describe("agent role badges name only roles the product can create", () => {
  afterEach(() => cleanup())

  // Without this, adding a role to SUPPORTED_ROLE_NAMES (or forgetting to add
  // a newly retired one to UNSUPPORTED_ROLE_NAMES) would silently shrink what
  // the checks below cover.
  it("the two vocabularies are disjoint", () => {
    for (const role of UNSUPPORTED_ROLE_NAMES) {
      expect(SUPPORTED_ROLE_NAMES).not.toContain(role)
    }
  })

  for (const surface of SURFACES) {
    describe(surface.name, () => {
      for (const role of UNSUPPORTED_ROLE_NAMES) {
        for (const variant of casings(role)) {
          it(`renders no badge promising ${variant}`, () => {
            surface.render(variant)
            expect(namesRole(renderedCopy(), role)).toBe(false)
          })
        }
      }

      it("still renders the agent it was given", () => {
        surface.render("COORDINATOR")
        expect(screen.getByText("Nova")).toBeInTheDocument()
      })

      it("still marks a LEAD", () => {
        surface.render("LEAD")
        expect(namesRole(renderedCopy(), "LEAD")).toBe(true)
      })
    })
  }
})

// The declared unions, checked here because nothing else checks them.
//
// It is tempting to say a third member added to AgentRole would be a compile
// error at the renderers. It would not: every renderer of a role declares
// `agent_role: string` on its own props on purpose — the value crosses a
// network boundary, where a union is a claim rather than a guarantee — so
// widening AgentRole type-checks tree-wide (`tsc --noEmit`, zero errors). Two
// individually correct decisions cancel the guarantee between them, which is
// exactly the kind of thing nobody re-derives while reading a diff.
//
// So this is the tripwire. A test cannot see a TypeScript union at runtime,
// so it reads the declarations.
describe("create-crew response types declare only creatable roles", () => {
  it("no agent_role literal in create-crew/api.ts names a role the create endpoint refuses", () => {
    const src = readFileSync(path.join(__dirname, "..", "create-crew", "api.ts"), "utf8")
    const declarations = [...src.matchAll(/^\s*agent_role:\s*(.+)$/gm)].map((m) => m[1])

    expect(declarations.length).toBeGreaterThan(0)
    for (const declaration of declarations) {
      for (const literal of [...declaration.matchAll(/"([^"]+)"/g)].map((m) => m[1])) {
        expect(SUPPORTED_ROLE_NAMES).toContain(literal)
      }
    }
  })

  // The declarations above reference the shared union rather than spelling the
  // members out, so this is where the members are pinned.
  it("the shared AgentRole union is exactly what the create endpoint accepts", () => {
    const src = readFileSync(path.join(__dirname, "..", "..", "..", "..", "lib", "agent-personas.ts"), "utf8")
    const declaration = src.match(/export type AgentRole\s*=\s*(.+)/)
    expect(declaration).not.toBeNull()

    const members = [...declaration![1].matchAll(/"([^"]+)"/g)].map((m) => m[1])
    expect([...members].sort()).toEqual([...SUPPORTED_ROLE_NAMES].sort())
  })
})

// The helper both renderers share is unit-tested in lib/__tests__. This file
// imports neither it nor anything else the fix introduced, so it runs
// unchanged against the code before the fix — which is where its 31 failures
// were recorded.
