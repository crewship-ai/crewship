import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import { join } from "node:path"
import { paletteDestinations } from "@/lib/palette-destinations"
import { parseIntegrationsRoute, SPACE_VIEWS } from "@/components/features/integrations/spaces"
import { settingsCardSlug } from "@/components/features/settings/shared"

const owner = paletteDestinations({ role: "OWNER", isInstanceAdmin: true })
const member = paletteDestinations({ role: "MEMBER", isInstanceAdmin: false })
const ids = (list: { id: string }[]) => list.map((d) => d.id)

describe("paletteDestinations", () => {
  it("gives every row its own id and a link", () => {
    expect(new Set(ids(owner)).size).toBe(owner.length)
    for (const d of owner) expect(d.href, d.id).toMatch(/^\//)
  })

  it("keeps what a role cannot open out of its palette", () => {
    // Danger zone is the owner's, Spend and invites are ADMIN+, Access &
    // Secrets is MANAGER+, the Admin console is instance administrators'.
    for (const id of ["settings:general:danger-zone", "journal:spend", "journal:timeline", "journal:runs", "act:invite", "settings:access-secrets:value-reveal", "admin:backups", "settings:audit"]) {
      expect(ids(owner), id).toContain(id)
      expect(ids(member), id).not.toContain(id)
    }
    expect(ids(member)).toContain("settings:sessions:cli-tokens")
    expect(ids(member)).toContain("act:issue")
  })

  it("links each settings card to a card that exists, by the slug its title makes", () => {
    const dir = join(process.cwd(), "components/features/settings/sections")
    const source = readdirSync(dir).filter((f) => f.endsWith(".tsx")).map((f) => readFileSync(join(dir, f), "utf8")).join("\n")
    const titles = [...source.matchAll(/<Settings(?:Danger)?Card[^>]*?title="([^"]+)"/gs)].map((m) => settingsCardSlug(m[1].replace(/&amp;/g, "&")))
    const cards = owner.filter((d) => d.href.includes("card="))
    expect(cards.length).toBeGreaterThan(10)
    for (const d of cards) {
      const slug = new URLSearchParams(d.href.split("?")[1]).get("card")
      expect(titles, `${d.id} → ${slug}`).toContain(slug)
    }
  })

  it("only points Integrations rows at spaces and views the page reads", () => {
    for (const d of owner.filter((x) => x.href.startsWith("/integrations"))) {
      const search = "?" + (d.href.split("?")[1] ?? "")
      const q = new URLSearchParams(search)
      const space = q.get("space") as keyof typeof SPACE_VIEWS
      expect(Object.keys(SPACE_VIEWS), d.id).toContain(space)
      if (q.get("view")) expect(SPACE_VIEWS[space] as readonly string[], d.id).toContain(q.get("view"))
      expect(parseIntegrationsRoute(search).space, d.id).toBe(space)
    }
  })
})
