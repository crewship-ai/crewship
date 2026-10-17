import { describe, it, expect } from "vitest"

import { envSecurity, envSummary, withSecurity, withoutFeature, withoutTool } from "../crew-env-config"

const DC = JSON.stringify({
  image: "mcr.microsoft.com/devcontainers/javascript-node:22",
  features: { "ghcr.io/devcontainers/features/github-cli:1": {}, "ghcr.io/devcontainers/features/docker-in-docker:2": { moby: false } },
  containerEnv: { TZ: "Europe/Prague" },
  privileged: true,
  capAdd: ["SYS_PTRACE"],
  customizations: { vscode: { extensions: ["x"] } },
})
const MISE = JSON.stringify({ env: { NODE_ENV: "production" }, tools: { node: "22.11.0", python: "3.12" } })

describe("crew environment config helpers", () => {
  it("summarises features, pinned tools and env without losing anything", () => {
    const s = envSummary(DC, MISE)
    expect(s.image).toBe("mcr.microsoft.com/devcontainers/javascript-node:22")
    expect(s.features).toEqual(["ghcr.io/devcontainers/features/github-cli:1", "ghcr.io/devcontainers/features/docker-in-docker:2"])
    expect(s.tools).toEqual([["node", "22.11.0"], ["python", "3.12"]])
    expect(s.env).toEqual([["TZ", "Europe/Prague"]])
    expect(s.miseVisual).toBe(true)
  })

  it("removing a feature keeps the image, the other features, security and unmodelled keys", () => {
    const next = JSON.parse(withoutFeature(DC, "ghcr.io/devcontainers/features/github-cli:1"))
    expect(Object.keys(next.features)).toEqual(["ghcr.io/devcontainers/features/docker-in-docker:2"])
    expect(next.features["ghcr.io/devcontainers/features/docker-in-docker:2"]).toEqual({ moby: false })
    expect(next.image).toBe("mcr.microsoft.com/devcontainers/javascript-node:22")
    expect(next.privileged).toBe(true)
    expect(next.capAdd).toEqual(["SYS_PTRACE"])
    expect(next.containerEnv).toEqual({ TZ: "Europe/Prague" })
    expect(next.customizations).toEqual({ vscode: { extensions: ["x"] } })
  })

  it("removing a tool keeps the other tools and the mise env", () => {
    const next = JSON.parse(withoutTool(MISE, "node"))
    expect(next.tools).toEqual({ python: "3.12" })
    expect(next.env).toEqual({ NODE_ENV: "production" })
  })

  it("a mise config the visual editor cannot read is left untouched", () => {
    const toml = '[tools]\nnode = "22"\n'
    expect(withoutTool(toml, "node")).toBe(toml)
  })

  it("security edits round-trip through devcontainer_config", () => {
    const sec = envSecurity(DC)
    expect(sec).toMatchObject({ privileged: true, init: false, capAdd: ["SYS_PTRACE"], containerEnv: { TZ: "Europe/Prague" } })
    const next = JSON.parse(withSecurity(DC, { ...sec, privileged: false, containerEnv: { TZ: "UTC", LANG: "C.UTF-8" } }))
    expect(next.privileged).toBeUndefined()
    expect(next.containerEnv).toEqual({ TZ: "UTC", LANG: "C.UTF-8" })
    expect(Object.keys(next.features)).toHaveLength(2)
    expect(next.customizations).toBeDefined()
  })
})
