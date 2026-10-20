import { describe, it, expect } from "vitest"
import { initialIntegrationsRoute } from "../integrations-layout"

// The old three-tab links still resolve; the full table lives in
// integrations-spaces.test.ts. This pins the export other modules import.

describe("initialIntegrationsRoute", () => {
  it("is the space route", () => {
    expect(initialIntegrationsRoute("?tab=notifications&section=deliveries")).toMatchObject({ space: "channels", view: "deliveries" })
    expect(initialIntegrationsRoute("?tab=incoming&section=agent&target=a1")).toMatchObject({ space: "triggers", starts: "agent", target: "a1" })
    expect(initialIntegrationsRoute("?tab=tools&section=crew-tools&server=s")).toMatchObject({ space: "tools", view: "crew-tools", server: "s" })
    expect(initialIntegrationsRoute("")).toMatchObject({ space: "tools", view: "connected" })
  })
})
