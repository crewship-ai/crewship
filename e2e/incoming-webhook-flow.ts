import { expect, type Page } from "@playwright/test"

/** Real HTTP setup and lifecycle; no route mocks or provider execution required. */
export async function incomingWebhookFlow(page: Page) {
  const slug = `e2e-incoming-${Date.now().toString(36)}`
  const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
  const workspaceId = workspaces[0]?.id
  expect(workspaceId).toBeTruthy()
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
    await page.goto("/integrations?tab=incoming")
    // Keep the app navigation's hover expansion away from the explorer.
    await page.mouse.move(1100, 600)
    await page
      .getByRole("button", { name: "Add integration", exact: true })
      .click()
    await page.getByRole("button", { name: /Incoming webhook/ }).click()
    const dialog = page.getByRole("dialog")
    await expect(dialog.getByRole("combobox", { name: "Target", exact: true })).toBeVisible()
    await dialog.getByRole("combobox", { name: "Target", exact: true }).click()
    await page.getByRole("option", { name: slug, exact: true }).click()
    await dialog
      .getByRole("textbox", { name: "Endpoint name" })
      .fill(`${slug} GitHub`)
    await dialog.getByRole("combobox", { name: "Sender" }).click()
    await page.getByRole("option", { name: "GitHub pull requests" }).click()
    const createdResponse = page.waitForResponse(
      (r) =>
        r.request().method() === "POST" &&
        r.url().includes("/pipeline-webhooks"),
    )
    await dialog.getByRole("button", { name: "Create endpoint" }).click()
    const created = await createdResponse
    expect(created.status()).toBe(201)
    const hook = await created.json()
    hooks.push(hook.id)
    expect(hook.ingress_profile).toBe("github")
    // Never attach the one-time token or signing secret to test output.
    await expect(dialog.getByTestId("incoming-receiving-url")).toContainText(
      "/github-pull-request",
    )
    await expect(
      dialog.getByRole("button", { name: "Copy receiving URL" }),
    ).toBeVisible()
    await dialog.getByRole("button", { name: "Done" }).click()
    await page.getByRole("button", { name: "Back to endpoints" }).click()
    await expect(
      page.getByText("Received · 24h", { exact: true }),
    ).toBeVisible()
    await page
      .getByRole("table")
      .getByRole("button", { name: `${slug} GitHub`, exact: true })
      .click()
    await expect(page).toHaveURL(new RegExp(`section=routine.*target=${slug}`))
    await page.reload()
    await expect(
      page.getByRole("heading", { name: slug, exact: true }),
    ).toBeVisible()
    await expect(
      page.getByText("/api/v1/webhooks/•••••/github-pull-request", {
        exact: true,
      }),
    ).toBeVisible()
    await expect(page.getByTestId("incoming-receiving-url")).toHaveCount(0)
    await page
      .getByRole("switch", { name: `Disable endpoint ${slug} GitHub` })
      .click()
    await expect
      .poll(async () => {
        const rows = await (
          await page.request.get(`${root}/pipeline-webhooks`)
        ).json()
        return rows.find((row: { id: string }) => row.id === hook.id)?.enabled
      })
      .toBe(false)
    // Add through the API while the detail remains open: Refresh must fetch it.
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
    await page.getByRole("button", { name: "Refresh", exact: true }).click()
    await expect(
      page.getByRole("table").getByText(`${slug} refreshed`, { exact: true }),
    ).toBeVisible()
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
