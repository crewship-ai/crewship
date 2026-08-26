import { describe, it, expect } from "vitest"
import { effectiveBaseImage, patchImage } from "../base-image"
import { INITIAL_STATE, type WizardState } from "../types"
import { DEFAULT_BASE_IMAGE } from "../../runtime-config-data"

/**
 * The wizard carries the base image in TWO fields — `runtimeImage`, which
 * submit sends, and `devcontainerConfig.image`, which RuntimeConfig reads
 * back — so `patchImage` is the one place they can disagree.
 */
const st = (patch: Partial<WizardState> = {}): WizardState => ({ ...INITIAL_STATE, ...patch })

describe("patchImage", () => {
  it("writes the image to both fields", () => {
    const out = patchImage(st(), "ghcr.io/org/img:1")
    expect(out.runtimeImage).toBe("ghcr.io/org/img:1")
    expect(JSON.parse(out.devcontainerConfig!).image).toBe("ghcr.io/org/img:1")
  })

  it("keeps the rest of a devcontainer document intact", () => {
    const existing = JSON.stringify({ image: "old:1", features: { "ghcr.io/x:1": {} } })
    const out = patchImage(st({ devcontainerConfig: existing }), "new:2")
    const dc = JSON.parse(out.devcontainerConfig!)
    expect(dc.image).toBe("new:2")
    expect(dc.features).toEqual({ "ghcr.io/x:1": {} })
  })

  it("leaves an operator's unparseable raw edit alone", () => {
    const out = patchImage(st({ devcontainerConfig: "{ not json" }), "new:2")
    expect(out.runtimeImage).toBe("new:2")
    expect(out.devcontainerConfig).toBeUndefined()
  })

  it("clearing the registry field removes the key rather than writing an empty one", () => {
    // The custom-image input calls onChange per keystroke, so select-all +
    // delete arrives here as "". Writing `"image": ""` gets the create refused
    // by Config.Validate (internal/devcontainer/config.go → ErrEmptyImage) —
    // while the row on screen reads "Debian slim, the shipped default",
    // because effectiveBaseImage falls back for DISPLAY only. The refusal
    // then looks unrelated to anything the user can see.
    const out = patchImage(
      st({
        runtimeImage: "ghcr.io/org/img:1",
        devcontainerConfig: JSON.stringify({ image: "ghcr.io/org/img:1", features: { "ghcr.io/x:1": {} } }),
      }),
      "",
    )
    expect(out.runtimeImage).toBe("")
    const dc = JSON.parse(out.devcontainerConfig!)
    expect(dc).not.toHaveProperty("image")
    // Only the image goes — the tooling the user picked stays.
    expect(dc.features).toEqual({ "ghcr.io/x:1": {} })
  })

  it("clearing it leaves nothing to send when there was no other config", () => {
    // An object with only an image key becomes an empty document, and
    // containerCreateBody skips an empty string — so the server applies its
    // own default instead of being handed a broken one.
    const out = patchImage(st({ runtimeImage: "x:1", devcontainerConfig: '{"image":"x:1"}' }), "")
    expect(out.devcontainerConfig).toBe("")
  })

  it("and the UI's fallback then agrees with what is sent", () => {
    const out = patchImage(st({ runtimeImage: "x:1" }), "")
    expect(effectiveBaseImage(st(out))).toBe(DEFAULT_BASE_IMAGE)
  })
})
