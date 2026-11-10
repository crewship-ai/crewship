/**
 * History, restore and links on a real server (#5144): G04, G05 and BE02 for Docs.
 *
 * G05: a restore is a new revision with the older content; later work stays in
 * history; a commenter cannot restore. BE02: an editor who was offline across
 * the restore must not merge its old-epoch edits into the restored document
 * without an explicit recovery step. G04: every historical link form ends on
 * /artifacts/<immutable id> once, with the person's own rights.
 */
import { expect, test, type Page } from "@playwright/test"
import { COMMENTER, EDITOR, OWNER, cli, closeAll, createDesign, createDoc, designNodes, grant, readDoc, requireThrowaway, screenshot, signIn, unique } from "./fixtures/collab"

test.beforeAll(() => requireThrowaway())

const editor = (page: Page) => page.locator("[data-docs-page] .ProseMirror")
const workspace = () => (JSON.parse(cli(["whoami", "-f", "json"])) as { workspace: { id: string } }).workspace.id

interface DocState { epoch: string; rev: number; schema: number; page_id?: string }
const state = (slug: string) => JSON.parse(readDoc(slug)) as DocState
const checkpoints = (slug: string) => (JSON.parse(cli(["doc", "checkpoints", slug, "-f", "json"])) as { checkpoints: { id: string; rev: number; actor: string; created_at: string }[] }).checkpoints

async function typeAtEnd(page: Page, text: string) {
  await editor(page).locator(":scope > p").first().click()
  await page.keyboard.press("End")
  await page.keyboard.type(text, { delay: 15 })
}

test("G05 + BE02 restore makes a new revision; an offline editor's old-epoch edit is not merged silently", async ({ browser }, info) => {
  const doc = createDoc(unique("g05"), ["Original text."])
  grant(doc.slug, EDITOR, "write"); grant(doc.slug, COMMENTER, "comment")
  const initial = checkpoints(doc.slug)[0]
  expect(initial.actor).toMatch(/^user:/)
  const b = await signIn(browser, EDITOR, info)
  const c = await signIn(browser, COMMENTER, info)
  try {
    await b.page.goto(`/artifacts/${doc.id}`)
    await expect(editor(b.page)).toBeVisible()
    await typeAtEnd(b.page, " [pre-restore]")
    await expect.poll(() => readDoc(doc.slug), { timeout: 20_000 }).toContain("[pre-restore]")
    await b.context.setOffline(true)
    await typeAtEnd(b.page, " [offline-old-epoch]")
    const before = state(doc.slug)
    // The commenter is refused; the owner restores the first checkpoint.
    const packet = { page_id: doc.id, id: unique("restore"), schema: before.schema, epoch: before.epoch, ifRev: before.rev, checkpoint: initial.id }
    const refused = await c.context.request.post(`/api/v1/pages/${doc.slug}/document/restore?workspace_id=${workspace()}`, { data: packet })
    expect([401, 403, 404, 405]).toContain(refused.status())
    cli(["doc", "restore", doc.slug, "--file", "-"], JSON.stringify(packet))
    const after = state(doc.slug)
    expect(after.rev).toBeGreaterThan(before.rev)
    expect(readDoc(doc.slug)).not.toContain("[pre-restore]")
    await b.context.setOffline(false)
    // Not merged without an explicit recovery step: the server never takes the
    // old-epoch edit, and the editor offers it as a draft instead of dropping it.
    await expect(b.page.locator("button[data-status]")).toHaveAttribute("data-status", "draft", { timeout: 30_000 })
    await screenshot(b.page, info, "g05-docs-editor-after-restore")
    expect(readDoc(doc.slug)).not.toContain("[offline-old-epoch]")
  } finally { await closeAll([b, c], info) }
})

test("G05 work replaced by a restore stays reachable in History", async () => {
  const doc = createDoc(unique("g05h"), ["Original text."])
  const initial = checkpoints(doc.slug)[0]
  const fresh = JSON.parse(readDoc(doc.slug)) as DocState & { blocks: { id: string; hash: string }[] }
  const block = fresh.blocks.find(item => item.id === "p1")!
  cli(["doc", "patch", doc.slug, "--file", "-"], JSON.stringify({ id: unique("patch"), schema: fresh.schema, page_id: doc.id, epoch: fresh.epoch, ifRev: fresh.rev, target: "p1", ifHash: block.hash, text: "Later text." }))
  expect(readDoc(doc.slug)).toContain("Later text.")
  const now = state(doc.slug)
  cli(["doc", "restore", doc.slug, "--file", "-"], JSON.stringify({ page_id: doc.id, id: unique("restore"), schema: now.schema, epoch: now.epoch, ifRev: now.rev, checkpoint: initial.id }))
  const listed = checkpoints(doc.slug)
  const contents = listed.map(pin => cli(["doc", "checkpoint", doc.slug, pin.id, "-f", "json"]))
  expect(contents.some(text => text.includes("Later text."))).toBe(true)
})

test("G04 a Docs revision link selects a real confirmed checkpoint without changing the live head",async({browser},info)=>{
  const doc=createDoc(unique('g04-doc-pin'),['Fixture initial text.'])
  const creation=checkpoints(doc.slug)[0]
  const replace=(text:string)=>{
    const current=JSON.parse(readDoc(doc.slug)) as DocState & {blocks:{id:string;hash:string}[]}
    cli(['doc','patch',doc.slug,'--file','-'],JSON.stringify({id:unique('live'),schema:current.schema,page_id:doc.id,epoch:current.epoch,ifRev:current.rev,target:'p1',ifHash:current.blocks.find(b=>b.id==='p1')!.hash,text}))
  }
  replace('Historical checkpoint text.')
  const historical=state(doc.slug)
  // A real supported restore pins the work it replaces; no SQL or private authority.
  cli(['doc','restore',doc.slug,'--file','-'],JSON.stringify({page_id:doc.id,id:unique('restore-fixture'),schema:historical.schema,epoch:historical.epoch,ifRev:historical.rev,checkpoint:creation.id}))
  const pin=checkpoints(doc.slug).find(p=>p.rev===historical.rev)!
  expect(pin).toBeTruthy();expect(pin.rev).toBeGreaterThan(0)
  expect(cli(['doc','checkpoint',doc.slug,pin.id,'-f','json'])).toContain('Historical checkpoint text.')
  replace('Different current live text.')
  const live=state(doc.slug);expect(live.rev).toBeGreaterThan(pin.rev)
  const a=await signIn(browser,OWNER,info)
  try{
    await a.page.goto(`/pages/${doc.slug}?version=${pin.rev}&workspace_id=${workspace()}`)
    await expect.poll(()=>new URL(a.page.url()).pathname).toBe(`/artifacts/${doc.id}`)
    expect(new URL(a.page.url()).searchParams.get('version')).toBe(String(pin.rev))
    const history=a.page.getByRole('region',{name:'Document history'})
    const shown=history.getByLabel(`Confirmed revision ${pin.rev}`,{exact:true})
    await expect(shown).toContainText('Historical checkpoint text.')
    await expect(shown).not.toContainText('Different current live text.')
    await expect(shown.locator('[contenteditable="true"]')).toHaveCount(0)
    expect(state(doc.slug).rev).toBe(live.rev);expect(readDoc(doc.slug)).toContain('Different current live text.')
    await screenshot(a.page,info,'g04-doc-confirmed-pin')
  }finally{await closeAll([a],info)}
})

test("G04 a Design version link opens the real read-only snapshot while the live canvas differs",async({browser},info)=>{
  const original={id:'r1',type:'rect',name:'Versioned layer',x:40,y:40,width:80,height:80,fill:'#3366ff'}
  const d=createDesign(unique('g04-design-pin'),[original])
  const pin=JSON.parse(cli(['page','design','snapshot',d.slug,'-f','json'])) as {version:number}
  cli(['page','design','apply',d.slug,'--file','-'],JSON.stringify({operations:[{op:'upsert',node:{...original,x:140}}]}))
  expect(designNodes(d.slug).find(n=>n.id==='r1')!.x).toBe(140)
  const a=await signIn(browser,OWNER,info)
  try{
    await a.page.goto(`/pages/${d.slug}?version=${pin.version}&workspace_id=${workspace()}`)
    await expect.poll(()=>new URL(a.page.url()).pathname).toBe(`/artifacts/${d.id}`)
    expect(new URL(a.page.url()).searchParams.get('version')).toBe(String(pin.version))
    const layer=a.page.locator('[data-design-node="r1"]');await expect(layer).toBeVisible()
    await layer.click();const x=a.page.getByLabel('x',{exact:true})
    await expect(x).toHaveValue('40');await expect(x).toBeDisabled()
    expect(designNodes(d.slug).find(n=>n.id==='r1')!.x).toBe(140)
    await screenshot(a.page,info,'g04-design-readonly-pin')
  }finally{await closeAll([a],info)}
})

test("G04 historical links end once on /artifacts/<id> with the person's own rights", async ({ browser }, info) => {
  const doc = createDoc(unique("g04"), ["Linked document."])
  grant(doc.slug, EDITOR, "read")
  const ws = workspace()
  const a = await signIn(browser, OWNER, info)
  const b = await signIn(browser, EDITOR, info)
  const c = await signIn(browser, COMMENTER, info)
  try {
    const links = [
      `/pages/${doc.slug}?workspace_id=${ws}`,
      `/pages/${doc.slug}`,
      `/pages/%40${doc.id}?workspace_id=${ws}`,
      `/pages?ref=${doc.id}&workspace_id=${ws}`,
      `/artifacts/${doc.id}`,
    ]
    for (const link of links) {
      const page = await a.context.newPage()
      await page.goto(link)
      await expect(page).toHaveURL(new RegExp(`/artifacts/${doc.id}(\\?|$)`))
      await expect(editor(page)).toContainText("Linked document.")
      // One replacement at most, and the address stays put: no redirect loop.
      const landed = page.url()
      await page.waitForTimeout(1_500)
      expect(page.url(), link).toBe(landed)
      expect(await page.evaluate(() => history.length), `${link} history entries`).toBeLessThanOrEqual(2)
      await page.close()
    }
    // A reader lands on the same address, read-only.
    await b.page.goto(`/pages/${doc.slug}?workspace_id=${ws}`)
    await expect(b.page).toHaveURL(new RegExp(`/artifacts/${doc.id}`))
    await expect(b.page.locator("button[data-status]")).toHaveAttribute("data-status", "readonly")
    // Someone without any grant gets no content through any link form.
    for (const link of [`/pages/${doc.slug}?workspace_id=${ws}`, `/artifacts/${doc.id}`]) {
      await c.page.goto(link)
      await c.page.waitForLoadState("networkidle")
      await expect(c.page.getByText("Linked document.")).toHaveCount(0)
    }
    await screenshot(c.page, info, "g04-no-grant-link")
  } finally { await closeAll([a, b, c], info) }
})
