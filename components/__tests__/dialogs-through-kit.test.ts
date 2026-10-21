import { describe, it, expect } from "vitest"
import { readFileSync, readdirSync, statSync } from "node:fs"
import path from "node:path"

// Harbor 2 dialog guard (PRD POP-01). Every create/edit/form dialog goes
// through `CreateSurface` (components/layout/create-surface.tsx) in its fixed
// widths sm/md/lg/xl; every confirmation goes through `ConfirmDialog`
// (components/ui/confirm-dialog.tsx). The raw Radix primitive in
// `@/components/ui/dialog` is how the product arrived at eleven widths and
// four title sizes, so outside the kit itself (components/ui, components/layout)
// a file may import it only when it is listed below with a reason.
//
// The list only shrinks: an entry whose file no longer imports the primitive
// fails too, so a migration has to take its line out.

const ROOT = path.resolve(__dirname, "../..")
const DIRS = ["app", "components", "lib", "hooks"]
const KIT = ["components/ui/", "components/layout/"]
const IMPORT = /from\s+["']@\/components\/ui\/dialog["']/

/** file -> why it is not a CreateSurface or a ConfirmDialog. */
export const ALLOWED: Record<string, string> = {
  // (d) Chat and Pages/Artifacts are owned by other teams; they migrate there.
  "components/features/chat/composer/slash-action-modal.tsx": "chat-owned",
  "components/features/chat/export/export-dialog.tsx": "chat-owned",
  "components/features/chat/turn-preference-feedback.tsx": "chat-owned",
  "components/features/conversations/unified-chat.tsx": "chat-owned",
  "components/features/conversations/workspace-conversations.tsx": "chat-owned",
  "components/features/pages/page-chat-handoff.tsx": "pages-owned",
  "components/features/pages/panels/panel-actions.tsx": "pages-owned",
  "components/features/pages/use-application-actions.tsx": "pages-owned",
}

function files(dir: string, out: string[] = []): string[] {
  let names: string[]
  try {
    names = readdirSync(dir)
  } catch {
    return out
  }
  for (const name of names) {
    const p = path.join(dir, name)
    if (name === "node_modules" || name === "__tests__" || name === "generated" || name.startsWith(".")) continue
    const st = statSync(p)
    if (st.isDirectory()) files(p, out)
    else if (/\.(tsx?|jsx?)$/.test(name) && !/\.(test|spec)\.[tj]sx?$/.test(name)) out.push(p)
  }
  return out
}

function importers(): string[] {
  const hits: string[] = []
  for (const d of DIRS) {
    for (const f of files(path.join(ROOT, d))) {
      const rel = path.relative(ROOT, f).split(path.sep).join("/")
      if (KIT.some((k) => rel.startsWith(k))) continue
      if (IMPORT.test(readFileSync(f, "utf8"))) hits.push(rel)
    }
  }
  return hits.sort()
}

describe("dialogs go through the kit", () => {
  const found = importers()

  it("no file outside the kit imports the raw dialog primitive unless allow-listed", () => {
    const unlisted = found.filter((f) => !(f in ALLOWED))
    expect(
      unlisted,
      "use CreateSurface (components/layout/create-surface.tsx) for a form, ConfirmDialog (components/ui/confirm-dialog.tsx) for a confirmation",
    ).toEqual([])
  })

  it("every allow-list entry still needs to be there", () => {
    const stale = Object.keys(ALLOWED).filter((f) => !found.includes(f))
    expect(stale, "migrated — remove the entry from ALLOWED").toEqual([])
  })

  it("every allow-list entry gives a reason", () => {
    for (const [file, why] of Object.entries(ALLOWED)) expect(why.trim(), file).not.toBe("")
  })

  it("the import pattern catches both quote styles and nothing else", () => {
    expect(IMPORT.test('import { Dialog } from "@/components/ui/dialog"')).toBe(true)
    expect(IMPORT.test("import { Dialog } from '@/components/ui/dialog'")).toBe(true)
    expect(IMPORT.test('import { AlertDialog } from "@/components/ui/alert-dialog"')).toBe(false)
    expect(IMPORT.test('import { ConfirmDialog } from "@/components/ui/confirm-dialog"')).toBe(false)
  })
})
