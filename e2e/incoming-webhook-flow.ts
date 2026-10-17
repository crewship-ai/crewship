import { expect, type Page } from "@playwright/test"

/** Real HTTP setup and lifecycle; no route mocks or provider execution required. */
export async function incomingWebhookFlow(page: Page) {
  const slug = `e2e-incoming-${Date.now().toString(36)}`
  const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
  const workspaceId = workspaces[0]?.id
  expect(workspaceId).toBeTruthy()
  const agents = await (await page.request.get(`/api/v1/agents?workspace_id=${workspaceId}`)).json()
  expect(agents.length).toBeGreaterThan(0)
  for (const agent of agents) expect(typeof agent.webhook_secret_set).toBe("boolean")

  const crews = await (
    await page.request.get(`/api/v1/crews?workspace_id=${workspaceId}`)
  ).json()
  expect(crews[0]?.id).toBeTruthy()
  const definition = {
    dsl_version: "1.0",
    name: slug,
    agentless: true,
    inputs: [{ name: "x", type: "string", default: "probe" }],
    steps: [
      {
        id: "noop",
        type: "transform",
        transform: { input: "{{ inputs.x }}", expression: "." },
      },
    ],
  }
  const root = `/api/v1/workspaces/${workspaceId}`
  const validation = await page.request.post(`${root}/pipelines/test_run`, {
    data: { definition, author_crew_id: crews[0].id },
  })
  expect(validation.ok()).toBeTruthy()
  const validated = await validation.json()
  expect(["DRY_RUN_OK", "COMPLETED"]).toContain(validated.status)
  const saved = await page.request.post(`${root}/pipelines/save`, {
    data: {
      slug,
      name: slug,
      definition,
      author_crew_id: crews[0].id,
      save_token: validated.save_token,
    },
  })
  expect(saved.ok()).toBeTruthy()
  const hooks: string[] = []
  try {
    await page.evaluate(
      (id) => localStorage.setItem("crewship.workspaceId", id),
      workspaceId,
    )
    // The old Incoming link still works: Harbor 2 (#3395, integrations N05/N06)
    // reads ?tab=incoming and rewrites it to the Triggers space.
    await page.goto("/integrations?tab=incoming")
    await expect(page).toHaveURL(/[?&]space=triggers/)
    // Keep the app navigation's hover expansion away from the explorer.
    await page.mouse.move(1100, 600)
    // The space's one primary action in the top bar: New trigger.
    const newTrigger = page.locator("[data-action=space-primary]")
    await expect(newTrigger).toHaveAccessibleName("New trigger")
    await newTrigger.click()
    const dialog = page.getByRole("dialog", { name: /New trigger/ })
    await expect(dialog).toBeVisible()
    // Step 1, Goes to: a routine, this test's own.
    await expect(dialog.getByRole("radio", { name: /^Routine/ })).toHaveAttribute("aria-checked", "true")
    await dialog.getByRole("combobox", { name: "Routine", exact: true }).click()
    await page.getByRole("option", { name: slug, exact: true }).click()
    await dialog.getByRole("button", { name: "Continue", exact: true }).click()
    // Step 2, Sender and mapping: GitHub checks the signature and takes only
    // pull_request events (the github ingress profile).
    await dialog.getByRole("radio", { name: /^GitHub/ }).click()
    await dialog.getByRole("textbox", { name: "Name" }).fill(`${slug} GitHub`)
    const createdResponse = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().includes("/pipeline-webhooks"),
    )
    await dialog.getByRole("button", { name: "Create", exact: true }).click()
    const created = await createdResponse
    expect(created.status()).toBe(201)
    const hook = await created.json()
    hooks.push(hook.id)
    expect(hook.ingress_profile).toBe("github")
    // Step 3, URL: shown once. Never attach the one-time token or signing
    // secret to test output.
    await expect(dialog.getByTestId("incoming-receiving-url")).toContainText(
      "/github-pull-request",
    )
    await expect(dialog.getByRole("button", { name: "Copy URL" })).toBeVisible()
    // While the secret shows, Esc does not throw it away; only Done closes.
    await page.keyboard.press("Escape")
    await expect(dialog.getByTestId("incoming-receiving-url")).toBeVisible()
    await dialog.getByRole("button", { name: "Done", exact: true }).click()
    await expect(dialog).toBeHidden()
    // Done lands on the triggers of the routine it starts.
    await expect(page).toHaveURL(new RegExp(`starts=routine.*target=${hook.target_pipeline_id}`))
    // A desktop list is a table; each row opens its trigger.
    await page
      .getByRole("row", { name: `Open ${slug} GitHub`, exact: true })
      .click()
    await expect(page).toHaveURL(new RegExp(`[?&]id=${hook.id}`))
    // The trigger page survives a reload and names what it starts.
    await page.reload()
    await expect(
      page.getByRole("heading", { name: `${slug} GitHub`, exact: true }),
    ).toBeVisible()
    await expect(page.getByText(`Starts routine ${slug}`, { exact: true })).toBeVisible()
    // After creation the URL is not shown again.
    await expect(
      page.getByText("Shown once, when the trigger was created", { exact: true }),
    ).toBeVisible()
    await expect(page.getByTestId("incoming-receiving-url")).toHaveCount(0)
    // Turning it off reaches the server.
    await page.getByRole("tab", { name: "Settings", exact: true }).click()
    await page.getByRole("switch", { name: "On", exact: true }).click()
    await expect
      .poll(async () => {
        const rows = await (
          await page.request.get(`${root}/pipeline-webhooks`)
        ).json()
        return rows.find((row: { id: string }) => row.id === hook.id)?.enabled
      })
      .toBe(false)
    // A trigger added elsewhere (the API, the CLI) shows in the list once it
    // is read again. Harbor 2 has no Refresh button on Triggers; a reload is
    // the read.
    const extra = await page.request.post(`${root}/pipeline-webhooks`, {
      data: {
        name: `${slug} refreshed`,
        target_pipeline_id: hook.target_pipeline_id,
        enabled: true,
      },
    })
    expect(extra.status()).toBe(201)
    const extraHook = await extra.json()
    hooks.push(extraHook.id)
    await page.getByRole("button", { name: "Triggers", exact: true }).click()
    await expect(page).not.toHaveURL(/[?&]id=/)
    await page.reload()
    await expect(
      page.getByRole("row", { name: `Open ${slug} refreshed`, exact: true }),
    ).toBeVisible()
    // The Tools space is a separate place: switching to it leaves the triggers.
    await page.getByRole("tablist", { name: "Spaces" }).getByRole("tab", { name: /^Tools/ }).click()
    await expect(page).toHaveURL(/[?&]space=tools/)
    await expect(
      page.getByRole("row", { name: `Open ${slug} refreshed`, exact: true }),
    ).toHaveCount(0)
  } finally {
    for (const id of hooks) {
      const deleted = await page.request.delete(
        `${root}/pipeline-webhooks/${id}`,
      )
      expect(deleted.ok()).toBeTruthy()
    }
    const deleted = await page.request.delete(`${root}/pipelines/${slug}`)
    expect(deleted.ok()).toBeTruthy()
  }
}
