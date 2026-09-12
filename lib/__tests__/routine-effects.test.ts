import { it, expect } from "vitest"
import { routineEffects } from "../routine-effects"
it("discloses nested actions without showing URL passwords, queries or credential values", () => {
  const effects = routineEffects({
    credentials_required: [{ type: "github" }],
    steps: [
      {
        type: "parallel",
        branches: [{ steps: [{ type: "agent_run", agent_slug: "writer" }] }],
      },
      {
        type: "http",
        http: {
          url: "https://user:secret@api.example.com/path?token=SECRET",
          credential_ref: { type: "service", value: "NEVER" },
        },
      },
      { type: "call", pipeline_slug: "child" },
    ],
  })
  expect(effects).toEqual({
    agents: ["writer"],
    hosts: ["api.example.com"],
    credentials: ["github", "service"],
    http: true,
    indirect: true,
  })
})
