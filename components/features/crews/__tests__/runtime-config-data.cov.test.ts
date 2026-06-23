import { describe, it, expect } from "vitest"
import {
  parseDevcontainerConfig,
  parseMiseConfig,
  buildDevcontainerJSON,
  buildMiseJSON,
  BASE_IMAGES,
  CATEGORY_FILTERS,
  CATEGORY_LABELS,
} from "../runtime-config-data"

// First direct coverage for the devcontainer/mise (de)serialization
// helpers extracted from runtime-config.tsx.

describe("parseDevcontainerConfig", () => {
  it("returns the slim Debian default for an empty string", () => {
    expect(parseDevcontainerConfig("")).toEqual({
      image: "debian:bookworm-slim",
      features: {},
    })
  })

  it("returns the default for malformed JSON", () => {
    expect(parseDevcontainerConfig("{nope")).toEqual({
      image: "debian:bookworm-slim",
      features: {},
    })
  })

  it("extracts image and features from a valid config", () => {
    const cfg = JSON.stringify({
      image: "mcr.microsoft.com/devcontainers/python:3.12-bookworm",
      features: { "ghcr.io/devcontainers/features/node:1": { version: "22" } },
    })
    expect(parseDevcontainerConfig(cfg)).toEqual({
      image: "mcr.microsoft.com/devcontainers/python:3.12-bookworm",
      features: { "ghcr.io/devcontainers/features/node:1": { version: "22" } },
    })
  })

  it("defaults missing image/features fields individually", () => {
    expect(parseDevcontainerConfig("{}")).toEqual({
      image: "debian:bookworm-slim",
      features: {},
    })
    expect(parseDevcontainerConfig('{"image":"x:1"}')).toEqual({
      image: "x:1",
      features: {},
    })
  })
})

describe("parseMiseConfig", () => {
  it("returns {} for empty input", () => {
    expect(parseMiseConfig("")).toEqual({})
  })

  it("returns {} for malformed JSON", () => {
    expect(parseMiseConfig("not json at all")).toEqual({})
  })

  it("returns {} when the tools key is absent", () => {
    expect(parseMiseConfig('{"other": 1}')).toEqual({})
  })

  it("extracts the tools map", () => {
    expect(parseMiseConfig('{"tools": {"node": "22", "go": "1.23"}}')).toEqual({
      node: "22",
      go: "1.23",
    })
  })
})

describe("buildDevcontainerJSON", () => {
  it("omits the features key when none are selected", () => {
    const out = buildDevcontainerJSON("debian:bookworm-slim", {})
    expect(JSON.parse(out)).toEqual({ image: "debian:bookworm-slim" })
    expect(out).not.toContain("features")
  })

  it("includes features when present and round-trips through the parser", () => {
    const features = { "ghcr.io/devcontainers/features/python:1": { version: "3.12" } }
    const out = buildDevcontainerJSON("img:1", features)
    expect(parseDevcontainerConfig(out)).toEqual({ image: "img:1", features })
  })
})

describe("buildMiseJSON", () => {
  it("returns an empty string for an empty tool map (clears the column)", () => {
    expect(buildMiseJSON({})).toBe("")
  })

  it("serializes tools and round-trips through the parser", () => {
    const out = buildMiseJSON({ node: "22" })
    expect(parseMiseConfig(out)).toEqual({ node: "22" })
  })
})

describe("catalog data", () => {
  it("has exactly one recommended base image and it is Node", () => {
    const recommended = BASE_IMAGES.filter((i) => i.recommended)
    expect(recommended).toHaveLength(1)
    expect(recommended[0].value).toContain("javascript-node")
  })

  it("every category filter except 'all' has a label", () => {
    for (const f of CATEGORY_FILTERS) {
      if (f === "all") continue
      expect(CATEGORY_LABELS[f], `label for ${f}`).toBeTruthy()
    }
  })

  it("base image values are unique registry paths", () => {
    const values = BASE_IMAGES.map((i) => i.value)
    expect(new Set(values).size).toBe(values.length)
  })
})
