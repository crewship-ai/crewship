import { test, expect } from "./fixtures/auth"

// One browser context is intentional. NextAuth rotates the session cookie on
// some authenticated mutations; six independent contexts would reuse the
// global-setup snapshot and eventually redirect to /login (or hit the login
// rate limit if each spec tried to repair that itself).
test("PR browser contract subset", async ({ page }) => {
  await test.step("login flow", async () => {
    await page.goto("/crews")
    await expect(page).toHaveURL(/\/crews/)
    await expect(page.getByRole("heading", { name: "Your fleet" })).toBeVisible()
  })

  await test.step("agent create dialog is reachable", async () => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Agent$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await expect(page.getByText(/^New agent$/)).toBeVisible()
    await page.getByRole("button", { name: /Cancel|Close/i }).first().click()
  })

  await test.step("create crew via wizard", async () => {
    const slug = `e2e-crew-${Date.now().toString(36)}`
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await page.getByPlaceholder("Engineering", { exact: true }).fill(`E2E Crew ${slug}`)
    await page.getByPlaceholder("engineering", { exact: true }).fill(slug)
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Start empty/ }).click()
    // Two Continues, not three. Lineup lands on Container now — the Runtime
    // step it used to pass through folded into it.
    await page.getByRole("button", { name: /Continue/ }).click()
    await page.getByRole("button", { name: /Skip to defaults/ }).click()
    await page.getByRole("button", { name: /Create crew/ }).click()
    await expect(page.getByRole("dialog")).not.toBeVisible()

    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    const crews = await (await page.request.get(`/api/v1/crews?workspace_id=${workspaceID}`)).json()
    const created = crews.find((crew: { id?: string; slug?: string }) => crew.slug === slug)
    expect(created?.id).toBeTruthy()
    // The server is disposable for this gate. Keep the created crew alive for
    // the following issue-create step; deleting it here can leave the UI's
    // workspace roster stale while the next modal is opening.
  })

  await test.step("cancel create-crew dialog", async () => {
    await page.goto("/crews")
    await page.getByRole("button", { name: /^Crew$/ }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    await page.getByRole("button", { name: /Cancel|Close/i }).first().click()
    await expect(page.getByRole("dialog")).not.toBeVisible()
  })

  await test.step("create issue and remove the fixture", async () => {
    const title = `E2E issue ${Date.now()}`
    await page.goto("/issues")
    await page.getByRole("button", { name: "New Issue", exact: true }).click()
    await expect(page.getByRole("dialog")).toBeVisible()
    // Selecting explicitly avoids relying on the modal's asynchronous
    // auto-select when the workspace roster is still loading.
    await page.getByRole("dialog").getByRole("button").first().click()
    await page.getByRole("option", { name: "Engineering", exact: true }).click()
    await page.getByPlaceholder("Issue title").fill(title)
    const createIssue = page.getByRole("button", { name: "Create issue", exact: true })
    await expect(createIssue).toBeEnabled({ timeout: 15_000 })
    const createResponse = page.waitForResponse(
      (response) => response.request().method() === "POST" && /\/issues(?:\?|$)/.test(response.url()),
    )
    await createIssue.click()
    const issueResponse = await createResponse
    expect(issueResponse.status(), await issueResponse.text()).toBe(201)
    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    await expect.poll(async () => {
      const data = await (await page.request.get(`/api/v1/issues?workspace_id=${workspaceID}`)).json()
      const rows = Array.isArray(data) ? data : data.rows ?? data.data ?? []
      return rows.find((issue: { title?: string; identifier?: string }) => issue.title === title)?.identifier ?? ""
    }, { timeout: 10_000 }).not.toBe("")
    const data = await (await page.request.get(`/api/v1/issues?workspace_id=${workspaceID}`)).json()
    const rows = Array.isArray(data) ? data : data.rows ?? data.data ?? []
    const identifier = rows.find((issue: { title?: string; identifier?: string }) => issue.title === title)?.identifier
    expect(identifier).toBeTruthy()
    await page.request.delete(
      `/api/v1/issues/${encodeURIComponent(identifier)}?workspace_id=${encodeURIComponent(workspaceID)}`,
    )
  })

  // #2398 — a run_needs_human card is acted on from /inbox.
  //
  // The card is written only when a run reports outcome NEEDS_HUMAN, which
  // needs a provider-backed agent this gate does not have (see the header of
  // playwright.pr.config.ts). So the inbox API is mocked at the browser: the
  // list serves one seeded card, the act door records what the page sent and
  // answers with B15's receipt shape. What this proves is the web side of
  // the contract — the card renders its actions[], Answer requires text, the
  // POST carries {action, input}, and the card flips to resolved with the
  // receipt without a reload. What it does NOT prove is the server: that the
  // answer reaches the session and that `inbox_acted` lands on the issue's
  // event log is covered by B15's Go + CLI acceptance tests (#2399,
  // internal/api/inbox_act.go and cmd/crewship inbox act), not here — a real
  // card needs a NEEDS_HUMAN run behind a provider this PR gate does not
  // have. A follow-up issue tracks a supported e2e fixture that could seed
  // one without a live agent run; until then this step is route-mocked.
  await test.step("inbox: act on a seeded run_needs_human card (route-mocked)", async () => {
    const workspaces = await (await page.request.get("/api/v1/workspaces")).json()
    const workspaceID: string = Array.isArray(workspaces) ? workspaces[0]?.id : workspaces.id
    expect(workspaceID).toBeTruthy()

    const cardID = "ibx_e2e_needs_human_1"
    const now = new Date().toISOString()
    const card = {
      id: cardID,
      workspace_id: workspaceID,
      kind: "run_needs_human",
      source_id: "asg_e2e_1",
      title: "Casey needs your input on ENG-7",
      body_md: "Which bucket should the export go to — staging or prod?",
      sender_type: "agent",
      sender_name: "Casey",
      state: "unread",
      priority: "high",
      blocking: true,
      attention_class: "input",
      thread_key: `issue:${workspaceID}:m_e2e_1`,
      actions: [
        { id: "answer", label: "Answer", effect: "Delivers your input to the agent's session and resumes the run from its checkpoint", irreversible: false },
        { id: "take_over", label: "Take over", effect: "Opens the issue for you to continue; the agent's session goes idle", irreversible: false },
        { id: "dismiss", label: "Dismiss", effect: "No further work now; the agent's session goes idle", irreversible: false },
      ],
      payload: { who_can_act: ["role:MANAGER"], context: { issue: "ENG-7", run: "asg_e2e_1" } },
      created_at: now,
      updated_at: now,
    }
    const receipt = {
      action: "answer",
      acted_by: "usr_e2e",
      acted_at: now,
      inbox_item_id: cardID,
      session_id: "ses_e2e_1",
      agent_version: 3,
      source_run_id: "asg_e2e_1",
      comment_id: "cmt_e2e_1",
      delivery_id: "mcm_e2e_1",
      run_id: "asg_e2e_2",
      dispatch_state: "dispatched",
      event_id: "act_e2e_1",
      seq: 14,
    }
    // Stateful on purpose: once acted, any refetch the page makes must see
    // the resolved card, the way the real server would answer.
    let acted = false
    const actBodies: unknown[] = []
    const resolvedCard = { ...card, state: "resolved", resolved_action: "answer", resolved_at: now, payload: { ...card.payload, receipt } }

    await page.route("**/api/v1/inbox?*", async (route) => {
      const rows = acted ? [] : [card]
      await route.fulfill({ json: { rows, count: rows.length, unread_count: acted ? 0 : 1, has_more: false } })
    })
    await page.route("**/api/v1/inbox/count?*", async (route) => {
      await route.fulfill({ json: { unread_count: acted ? 0 : 1 } })
    })
    await page.route(`**/api/v1/inbox/${cardID}?*`, async (route) => {
      if (route.request().method() === "PATCH") {
        await route.fulfill({ json: { id: cardID, state: "read" } })
        return
      }
      await route.fulfill({ json: acted ? resolvedCard : card })
    })
    await page.route(`**/api/v1/inbox/${cardID}/act?*`, async (route) => {
      actBodies.push(route.request().postDataJSON())
      acted = true
      await route.fulfill({ json: { id: cardID, state: "resolved", action: "answer", receipt } })
    })

    await page.goto(`/inbox?item=${cardID}`)
    await expect(page.getByTestId(`row-${cardID}`)).toBeVisible()
    const pane = page.getByTestId("reading-pane")
    await expect(pane.getByText("Casey needs your input on ENG-7")).toBeVisible()
    // The §12 badge, and the three actions the card carries.
    await expect(pane.getByTestId("attention-badge")).toHaveText("Input needed")
    await expect(pane.getByRole("button", { name: "Take over" })).toBeVisible()
    await expect(pane.getByRole("button", { name: "Dismiss" })).toBeVisible()

    await pane.getByRole("button", { name: "Answer" }).click()
    const send = pane.getByRole("button", { name: "Send" })
    await expect(send).toBeDisabled()
    await pane.getByRole("textbox", { name: "Your answer" }).fill("Use the staging bucket, not prod.")
    await expect(send).toBeEnabled()
    await send.click()

    // Resolved in place, with the receipt — no navigation, no reload.
    const rec = pane.getByTestId("act-receipt")
    await expect(rec).toBeVisible()
    await expect(rec).toContainText("asg_e2e_2")
    await expect(rec).toContainText("event #14")
    await expect(pane.getByRole("button", { name: "Send" })).toHaveCount(0)
    await expect(pane.getByText(/^Resolved /)).toBeVisible()
    expect(page.url()).toContain("/inbox")
    expect(actBodies).toEqual([{ action: "answer", input: "Use the staging bucket, not prod." }])

    await page.unrouteAll({ behavior: "ignoreErrors" })
  })
})
