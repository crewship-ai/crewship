import { describe, expect, it } from "vitest"
import { readFileSync, readdirSync } from "node:fs"
import { join } from "node:path"
import { paletteDestinations } from "@/lib/palette-destinations"
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
    for (const id of ["settings:general:danger-zone", "journal:spend", "act:invite", "settings:access-secrets:value-reveal", "admin:backups", "settings:audit"]) {
      expect(ids(owner), id).toContain(id)
      expect(ids(member), id).not.toContain(id)
    }
    expect(ids(member)).toContain("settings:profile:sessions-and-access")
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

  it("only points Integrations rows at sections the page reads", () => {
    const sections: Record<string, string[]> = {
      notifications: ["connections", "preferences", "deliveries"],
      incoming: ["endpoints", "routine", "agent", "page"],
      tools: ["catalog", "accounts", "agents", "tools", "triggers", "mcp", "crew-tools"],
    }
    for (const d of owner.filter((x) => x.href.startsWith("/integrations"))) {
      const q = new URLSearchParams(d.href.split("?")[1])
      expect(sections[q.get("tab") ?? ""], d.id).toContain(q.get("section"))
    }
  })
})
