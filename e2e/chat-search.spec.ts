import { test, expect, type Page } from "@playwright/test"
import { DatabaseSync } from "node:sqlite"
import { appendFileSync, mkdirSync, readFileSync, writeFileSync } from "node:fs"
import { join } from "node:path"
import { throwawayRefusal } from "./throwaway-guard"
import { randomUUID } from "node:crypto"

// Chat search (CH-25) against a REAL backend: the real POST
// /api/v1/conversations/search, the real transcript endpoint, the real chat
// page. Nothing is intercepted.
//
// The one thing the product cannot produce on a provider-free instance is an
// assistant message, so the transcripts are written the way the product's own
// store writes them (conversation.Store.Append): one JSONL line under
// <data dir>/output/conversations/<chat id>.jsonl plus the row of the search
// mirror, conversation_messages (its FTS5 triggers index it). The chats
// themselves are created through the real API.
//
// Because it writes into the data directory directly, the suite fails closed:
// it runs only under scripts/throwaway-server.sh (CREWSHIP_THROWAWAY_MANIFEST)
// and skips, with the reason, unless E2E_CREWSHIP_DATA_DIR is exactly that
// instance's data dir under the throwaway state root (never a shared env or
// dev slot), the manifest is running, and the base URL is that instance's
// server (host and port). A last read-only probe checks the backend behind the base URL knows
// a workspace that is in that database before anything is written. Run:
//
//   scripts/throwaway-server.sh run NAME --binary PATH -- sh -c \
//     'E2E_CREWSHIP_DATA_DIR=$(jq -r .data_dir "$CREWSHIP_THROWAWAY_MANIFEST") \
//      PLAYWRIGHT_BASE_URL=$CREWSHIP_SERVER pnpm exec playwright test e2e/chat-search.spec.ts'
//
// (the server's embedded UI must be a build of this checkout: `pnpm build`,
// `scripts/embed-web-out.sh sync`, `go build ./cmd/crewship`, then pass that
// binary to --binary; the base URL is always the throwaway server itself.)

const TIMEOUT = 20_000
const DATA_DIR = process.env.E2E_CREWSHIP_DATA_DIR ?? ""

/** Data dirs of the shared dev slots, from the host's environment registry when present. */
function sharedSlotDataDirs(): string[] {
  try {
    const envs = JSON.parse(readFileSync("/etc/crewship/environments.json", "utf8")) as Record<string, Record<string, unknown>>
    return Object.values(envs).flatMap((e) => ["state_root", "data_dir", "dataDir"].map((k) => e?.[k]).filter((v): v is string => typeof v === "string"))
  } catch { return [] }
}

const REFUSAL = throwawayRefusal(process.env, process.env.PLAYWRIGHT_BASE_URL ?? "", sharedSlotDataDirs())
test.skip(REFUSAL !== "", `chat-search e2e writes into the data dir and only runs on a verified throwaway server: ${REFUSAL}`)
const KEY = `zq${Date.now().toString(36)}`
const T = {
  general: `${KEY}a`,
  long: `${KEY}l`,
  many: `${KEY}m`,
  gone: `${KEY}d`,
}

interface Agent { id: string; slug: string; name: string }
interface Seed {
  workspaceId: string
  agents: Agent[]
  /** hit ids for the single-message cases */
  longTargetId: string
  longLastId: string
  goneId: string
  goneChatId: string
  goneFile: string
  assistantCount: number
  userCount: number
}

let seed: Seed

function seedMessages(
  db: DatabaseSync,
  chatId: string,
  agentId: string,
  rows: { id?: string; role: "user" | "assistant"; content: string }[],
) {
  const dir = join(DATA_DIR, "output", "conversations")
  mkdirSync(dir, { recursive: true })
  const file = join(dir, `${chatId}.jsonl`)
  const insert = db.prepare(
    "INSERT INTO conversation_messages (id, session_id, agent_id, role, content, tool_summary, ts) VALUES (?, ?, ?, ?, ?, '', ?)",
  )
  const base = Date.now() - 3_600_000
  const ids: string[] = []
  rows.forEach((m, i) => {
    const id = m.id ?? `m-${randomUUID()}`
    const ts = new Date(base + i * 1000).toISOString()
    appendFileSync(
      file,
      JSON.stringify({ id, session_id: chatId, agent_id: agentId, role: m.role, content: m.content, ts }) + "\n",
    )
    insert.run(id, chatId, agentId, m.role, m.content, ts.replace(/\.(\d{3})Z$/, ".$1Z"))
    ids.push(id)
  })
  return { ids, file }
}

async function createChat(page: Page, workspaceId: string, agentId: string): Promise<string> {
  const id = randomUUID()
  const status = await page.evaluate(
    async ([ws, agent, sid]) => {
      const res = await fetch(`/api/v1/agents/${agent}/chats?workspace_id=${ws}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ session_id: sid, origin: "UI" }),
      })
      return res.status
    },
    [workspaceId, agentId, id] as const,
  )
  expect(status, "chat create").toBeLessThan(300)
  return id
}

function filler(n: number, prefix: string) {
  return Array.from({ length: n }, (_, i) => ({
    role: (i % 2 === 0 ? "user" : "assistant") as "user" | "assistant",
    content: `${prefix} filler message ${i} about nothing in particular, long enough to take real space in the transcript view`,
  }))
}

test.beforeAll(async ({ browser }) => {
  test.skip(REFUSAL !== "", REFUSAL)
  const ctx = await browser.newContext({ storageState: test.info().project.use.storageState as string })
  const page = await ctx.newPage()
  await page.goto("/")
  const found = await page.evaluate(async () => {
    const ws = (await (await fetch("/api/v1/workspaces")).json())[0]
    const agents = await (await fetch(`/api/v1/agents?workspace_id=${ws.id}`)).json()
    return {
      workspaceId: ws.id as string,
      agents: (agents as Agent[]).filter((a) => a.slug).map((a) => ({ id: a.id, slug: a.slug, name: a.name })),
    }
  })
  expect(found.agents.length).toBeGreaterThanOrEqual(5)
  // Read-only: the backend behind the base URL must be the one that owns DATA_DIR.
  const probe = new DatabaseSync(join(DATA_DIR, "crewship.db"), { readOnly: true })
  try {
    const row = probe.prepare("SELECT 1 AS ok FROM agents WHERE id = ?").get(found.agents[0].id)
    if (!row) throw new Error("refusing to write: the agent served at the base URL is not in E2E_CREWSHIP_DATA_DIR's database")
  } finally { probe.close() }
  const [a0, a1, a2, a3, a4] = found.agents
  const db = new DatabaseSync(join(DATA_DIR, "crewship.db"))
  db.exec("PRAGMA busy_timeout = 5000")
  try {
    // General: user and assistant messages carrying the same word, in two agents' chats.
    const c0 = await createChat(page, found.workspaceId, a0.id)
    seedMessages(db, c0, a0.id, [
      { role: "user", content: `${T.general} plan, please` },
      { role: "assistant", content: `Here is the ${T.general} plan in three steps.` },
      { role: "user", content: `And the ${T.general} budget?` },
      { role: "assistant", content: `The ${T.general} budget is approved.` },
    ])
    const c1 = await createChat(page, found.workspaceId, a1.id)
    seedMessages(db, c1, a1.id, [
      { role: "user", content: `Any news on ${T.general}?` },
      { role: "assistant", content: `Yes: ${T.general} shipped.` },
    ])

    // Long transcript: the hit is near the top, the chat opens at the bottom.
    const c2 = await createChat(page, found.workspaceId, a2.id)
    const long = filler(60, "long")
    long[5] = { role: "assistant", content: `The decisive note about ${T.long} is here.` }
    const longSeed = seedMessages(db, c2, a2.id, long)

    // More than one page of hits.
    const c3 = await createChat(page, found.workspaceId, a3.id)
    seedMessages(
      db,
      c3,
      a3.id,
      Array.from({ length: 55 }, (_, i) => ({
        role: (i % 2 === 0 ? "user" : "assistant") as "user" | "assistant",
        content: `Note ${i} mentions ${T.many} once`,
      })),
    )

    // A hit whose transcript line is removed before it is opened.
    const c4 = await createChat(page, found.workspaceId, a4.id)
    const goneSeed = seedMessages(db, c4, a4.id, [
      { role: "user", content: "hello" },
      { role: "assistant", content: `This ${T.gone} reply will be deleted.` },
      { role: "user", content: "and a later message that stays" },
    ])
    seed = {
      ...found,
      longTargetId: longSeed.ids[5],
      longLastId: longSeed.ids[59],
      goneId: goneSeed.ids[1],
      goneChatId: c4,
      goneFile: goneSeed.file,
      assistantCount: 3,
      userCount: 3,
    }
  } finally {
    db.close()
  }
  await ctx.close()
})

async function openSearch(page: Page, slug: string) {
  await page.goto(`/chat/${slug}`)
  await expect(page.getByPlaceholder(/^Message /)).toBeVisible({ timeout: TIMEOUT })
  const dialog = page.getByRole("dialog")
  // The shortcut is registered on hydration; a key pressed before that is lost, so press again until it opens.
  await expect(async () => {
    await page.keyboard.press("Control+Shift+F")
    await expect(dialog.getByRole("combobox", { name: "Search query" })).toBeVisible({ timeout: 1500 })
  }).toPass({ timeout: TIMEOUT })
  return dialog
}

const options = (page: Page) => page.getByRole("dialog").getByRole("option")

test("Ctrl+Shift+F opens search; a plain query returns user and assistant hits", async ({ page }) => {
  const dialog = await openSearch(page, seed.agents[0].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(T.general)
  await expect(options(page)).toHaveCount(seed.assistantCount + seed.userCount, { timeout: TIMEOUT })
  const labels = await options(page).evaluateAll((els) => els.map((e) => e.querySelector("span > span")?.textContent ?? ""))
  expect(labels.filter((l) => l === "User")).toHaveLength(seed.userCount)
})

test("from:agent returns only assistant messages", async ({ page }) => {
  const dialog = await openSearch(page, seed.agents[0].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(`${T.general} from:agent`)
  await expect(options(page)).toHaveCount(seed.assistantCount, { timeout: TIMEOUT })
  const labels = await options(page).evaluateAll((els) => els.map((e) => e.querySelector("span > span")?.textContent ?? ""))
  expect(labels).not.toContain("User")
  // The two agents that wrote them are both named.
  expect(new Set(labels).size).toBe(2)
})

test("from:me shows the not-supported hint and no results", async ({ page }) => {
  const dialog = await openSearch(page, seed.agents[0].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(`${T.general} from:me`)
  const hint = dialog.getByRole("alert")
  await expect(hint).toContainText("from:me is not supported", { timeout: TIMEOUT })
  await expect(options(page)).toHaveCount(0)
})

test("Enter opens the exact message, scrolled into view", async ({ page }) => {
  const dialog = await openSearch(page, seed.agents[2].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(T.long)
  await expect(options(page)).toHaveCount(1, { timeout: TIMEOUT })
  await expect(options(page).first()).toHaveAttribute("aria-selected", "true")
  await page.getByRole("combobox", { name: "Search query" }).press("Enter")
  await expect(page).toHaveURL(new RegExp(`message=${seed.longTargetId}`), { timeout: TIMEOUT })

  const target = page.locator(`[data-turn-id="${seed.longTargetId}"]`)
  await expect(target).toBeVisible({ timeout: TIMEOUT })
  const vh = page.viewportSize()!.height
  const inViewport = async (id: string) => {
    const box = await page.locator(`[data-turn-id="${id}"]`).boundingBox()
    return box !== null && box.y >= 0 && box.y + box.height <= vh
  }
  await expect
    .poll(() => inViewport(seed.longTargetId), { timeout: TIMEOUT, message: "target message is not inside the viewport" })
    .toBe(true)
  // The transcript opened at the bottom and was scrolled away from it.
  expect(await inViewport(seed.longLastId)).toBe(false)
  await expect(target).toHaveAttribute("data-search-target", "true")
})

test("a hit whose message was deleted shows the notice", async ({ page }) => {
  // Search from a different agent's chat so the target transcript is not already loaded.
  const dialog = await openSearch(page, seed.agents[0].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(T.gone)
  await expect(options(page)).toHaveCount(1, { timeout: TIMEOUT })
  // Delete the message from the transcript after the search has found it.
  const file = seed.goneFile
  const kept = readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line && !line.includes(seed.goneId))
  writeFileSync(file, kept.join("\n") + "\n")
  await page.getByRole("combobox", { name: "Search query" }).press("Enter")
  await expect(page).toHaveURL(new RegExp(`message=${seed.goneId}`), { timeout: TIMEOUT })
  await expect(page.getByText("Could not locate that message in this conversation view.")).toBeVisible({ timeout: 15_000 })
})

test("fifty or more hits say the first 50 are shown", async ({ page }) => {
  const dialog = await openSearch(page, seed.agents[3].slug)
  await dialog.getByRole("combobox", { name: "Search query" }).fill(T.many)
  await expect(options(page)).toHaveCount(50, { timeout: TIMEOUT })
  await expect(dialog.getByText("First 50 results, more may exist.")).toBeVisible()
})
