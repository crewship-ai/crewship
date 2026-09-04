import { describe, it, expect } from "vitest"
import { crewContainerSummary, formatCrewDate, networkModeLabel } from "@/components/features/crews/crew-summary"

const crew = { runtime_image: null, container_memory_mb: 4096, container_cpus: 2, container_ttl_hours: null, network_mode: "restricted" }

describe("crewContainerSummary", () => {
  it("says nothing about a TTL that is not set, and names the network in words", () => {
    expect(crewContainerSummary(crew)).toBe("debian:trixie-slim · 4.0 GB · 2 CPU · Restricted network")
  })
  it("says when the container stops when a TTL is set", () => {
    expect(crewContainerSummary({ ...crew, container_ttl_hours: 8, network_mode: "free", runtime_image: "mcr.microsoft.com/devcontainers/javascript-node:22" }))
      .toBe("mcr.microsoft.com/devcontainers/javascript-node:22 · 4.0 GB · 2 CPU · stops after 8h idle · Open network")
  })
  it("never invents a label for a mode it does not know", () => {
    expect(networkModeLabel("weird")).toBe("weird network")
  })
})

describe("formatCrewDate", () => {
  it("reads as day, month, year, never a numeric slash date", () => {
    expect(formatCrewDate("2026-09-03T10:00:00Z", "en-GB")).toBe("3 Sept 2026")
    expect(formatCrewDate("not a date")).toBe("—")
  })
})
