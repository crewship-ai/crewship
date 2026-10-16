import { expect, type Page } from "@playwright/test"

/** Real authenticated browser → actual public Router/storage. Only the lost
 * acknowledgement is intercepted, after the server has committed it. */
export async function staticArtifactHumanFlow(page: Page) {
  const slug = `static-${Date.now().toString(36)}`
  const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
  const workspace = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
  await page.goto("/pages")
  await page.getByRole("button", { name: "New HTML", exact: true }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByLabel("Name", { exact: true }).fill("Static browser report")
  await dialog.getByLabel("Slug", { exact: true }).fill(slug)
  await dialog.getByRole("button", { name: "Create HTML Page" }).click()
  await expect(dialog).not.toBeVisible()
  const pane = page.getByRole("region", { name: "HTML artifact" })
  await expect(pane.getByText("No HTML saved yet.", { exact: false })).toBeVisible()
  const record = await (await page.request.get(`/api/v1/pages/${slug}?workspace_id=${workspace}`)).json()
  expect(record.id).toBeTruthy()
  const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAIAAAADCAYAAAC56t6BAAAAEUlEQVR4nGNQSFjwH4QZMBgAo1MMu8a2NUsAAAAASUVORK5CYII="
  const html = `<h1>Žluťoučký report</h1><table><tr><td>Čeština</td><td>Tabulka</td></tr></table><img alt="Local raster" src="${png}"><script>window.unsafeArtifact=true</script><iframe src="https://static-sink.invalid/frame"></iframe><img src="https://static-sink.invalid/image"><a href="https://static-sink.invalid/link">No navigation</a>`
  const foreign: string[] = []
  const observe = (request: { url(): string }) => { if (request.url().includes("static-sink.invalid")) foreign.push(request.url()) }
  page.on("request", observe)
  try {
    await pane.getByLabel("Upload HTML").setInputFiles({ name: "report.html", mimeType: "text/html", buffer: Buffer.from(html) })
    await expect(pane.getByLabel(/HTML for the next revision/)).toHaveValue(html)
    const attempts: string[] = []
    let lose = true
    const routePattern = `**/api/v1/pages/${slug}/artifact/revisions?**`
    await page.route(routePattern, async route => {
      attempts.push(route.request().postData()!)
      if (lose) { lose = false; const committed = await route.fetch(); expect(committed.status()).toBe(201); await route.abort("connectionfailed") }
      else await route.continue()
    })
    await pane.getByRole("button", { name: "Save revision", exact: true }).click()
    await expect(pane.getByRole("button", { name: "Retry exact save" })).toBeVisible()
    await pane.getByRole("button", { name: "Retry exact save" }).click()
    await expect(pane.getByText("Revision 1 · latest 1", { exact: true })).toBeVisible()
    expect(attempts).toHaveLength(2)
    expect(attempts[1]).toBe(attempts[0])
    expect(JSON.parse(attempts[0])).toMatchObject({ page_id: record.id, base_rev: 0, html })
    await page.unroute(routePattern)
    const frame = page.frameLocator('iframe[title="Artifact static preview"]')
    await expect(frame.getByRole("heading", { name: "Žluťoučký report" })).toBeVisible()
    await expect(frame.getByRole("cell", { name: "Čeština" })).toBeVisible()
    await expect(frame.getByAltText("Local raster")).toBeVisible()
    expect(await frame.locator("script,iframe,[href],[onclick]").count()).toBe(0)
    expect(await frame.getByAltText("Local raster").evaluate((node: HTMLImageElement) => ({ width: node.naturalWidth, height: node.naturalHeight }))).toEqual({ width: 2, height: 3 })
    // A bounded idle fence catches late image/navigation attempts after mount.
    await page.waitForLoadState("networkidle")
    expect(foreign).toEqual([])
    await pane.getByRole("button", { name: "Source", exact: true }).click()
    await pane.getByRole("button", { name: "Edit as new revision" }).click()
    await pane.getByLabel(/HTML for the next revision/).fill("<h1>Second revision</h1>")
    await pane.getByRole("button", { name: "Save revision", exact: true }).click()
    await expect(pane.getByText("Revision 2 · latest 2", { exact: true })).toBeVisible()
    await pane.getByRole("button", { name: "History", exact: true }).click()
    await pane.getByRole("button", { name: "Restore revision 1", exact: true }).click()
    await expect(pane.getByText("Revision 3 · latest 3", { exact: true })).toBeVisible()
    await expect(frame.getByRole("heading", { name: "Žluťoučký report" })).toBeVisible()
    await pane.getByRole("button", { name: "History", exact: true }).click()
    await expect(pane.getByText("Revision 3 · restored from 1", { exact: true })).toBeVisible()
  } finally {
    page.off("request", observe)
    const removed = await page.request.delete(`/api/v1/pages/${slug}?workspace_id=${workspace}`)
    expect(removed.status()).toBe(204)
  }
}
