import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"
import { SubscriptionsPanel } from "./subscriptions-panel"

describe("SubscriptionsPanel payer attribution", () => {
  it("shows unused subscription seats with their owner, without treating API keys as seats", () => {
    render(<SubscriptionsPanel loading={false} rows={[]} logins={[
      { credential_id: "unused", name: "Unused subscription", login: { mode: "subscription", provider: "OPENAI", plan_label: "ChatGPT Plus", owner_email: "owner@example.test" } },
      { credential_id: "metered", name: "API key account", login: { mode: "api_key", provider: "OPENAI" } },
    ]} />)
    expect(screen.getByText("Unused subscription")).toBeTruthy()
    expect(screen.getByText("owner@example.test")).toBeTruthy()
    expect(screen.getByText("0 calls")).toBeTruthy()
    expect(screen.queryByText("API key account")).toBeNull()
  })
  it("keeps same-plan logins and unknown historical usage separate", () => {
    const common = {
      subscription_plan: "ChatGPT Plus", provider: "openai", call_count: 1,
      input_tokens: 100, output_tokens: 20, last_ts: "",
    }
    render(<SubscriptionsPanel
      loading={false}
      rows={[
        { ...common, credential_id: "login-a" },
        { ...common, credential_id: "login-b" },
        common,
      ]}
      logins={[
        { credential_id: "login-a", name: "Team account A" },
        { credential_id: "login-b", name: "Team account B" },
      ]}
    />)
    expect(screen.getByText("Team account A")).toBeTruthy()
    expect(screen.getByText("Team account B")).toBeTruthy()
    expect(screen.getByText("Login unknown")).toBeTruthy()
    expect(screen.getAllByRole("listitem")).toHaveLength(3)
    expect(screen.queryByText("$0")).toBeNull()
  })
})
