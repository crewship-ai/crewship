import { describe, it, expect } from "vitest"

import { describeUserAgent } from "@/lib/user-agent"

// This string is the only thing standing between "Chrome on macOS" and an
// unreadable 120-character blob in a screen whose whole job is letting
// someone spot a login that isn't theirs. Precision matters more than
// coverage: a confidently WRONG label ("Safari on Windows") is worse than
// an honest unknown, because it talks the user out of investigating.

describe("describeUserAgent", () => {
  it.each([
    [
      "Chrome on macOS",
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
    ],
    [
      "Safari on iPhone",
      "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
    ],
    [
      "Safari on iPad",
      "Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
    ],
    [
      "Safari on macOS",
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
    ],
    [
      "Firefox on Windows",
      "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:127.0) Gecko/20100101 Firefox/127.0",
    ],
    [
      "Chrome on Android",
      "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
    ],
    [
      "Chrome on Linux",
      "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
    ],
  ])("reads %s", (expected, ua) => {
    expect(describeUserAgent(ua).label).toBe(expected)
  })

  // Every one of these lies about being something else. Getting the order
  // wrong is THE classic user-agent bug, so each gets its own case.
  it("does not call Edge 'Chrome' — its UA contains Chrome/", () => {
    const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0"
    expect(describeUserAgent(ua).label).toBe("Edge on Windows")
  })

  it("does not call Chrome 'Safari' — its UA ends in Safari/", () => {
    const ua = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
    expect(describeUserAgent(ua).label).not.toContain("Safari")
  })

  it("does not call Opera 'Chrome'", () => {
    const ua = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 OPR/112.0.0.0"
    expect(describeUserAgent(ua).label).toBe("Opera on Windows")
  })

  it("does not call Android 'Linux' — its UA says Linux", () => {
    const ua = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36"
    expect(describeUserAgent(ua).label).toContain("Android")
    expect(describeUserAgent(ua).label).not.toContain("Linux")
  })

  it("recognises the Crewship CLI by name and marks it as a CLI", () => {
    const d = describeUserAgent("crewship/0.9.3 (darwin/arm64)")
    expect(d.label).toBe("Crewship CLI on macOS")
    expect(d.kind).toBe("cli")
  })

  it("keeps a dev-build CLI readable", () => {
    expect(describeUserAgent("crewship/dev (linux/amd64)").label).toBe("Crewship CLI on Linux")
  })

  // An unattributed Go client is almost certainly our own CLI, but "almost
  // certainly" is not good enough to print as fact on a security screen.
  it("does not claim a bare Go client is the Crewship CLI", () => {
    const d = describeUserAgent("Go-http-client/2.0")
    expect(d.label).not.toContain("Crewship")
    expect(d.kind).toBe("unknown")
  })

  it.each([
    ["", "Unknown device"],
    ["   ", "Unknown device"],
  ])("falls back for an empty user agent (%s)", (ua, expected) => {
    expect(describeUserAgent(ua).label).toBe(expected)
  })

  it("shows an unrecognised agent verbatim rather than guessing", () => {
    const d = describeUserAgent("SomeNewBrowser/1.0 (Fuchsia)")
    // Honest: the raw string is more use to someone hunting an intruder
    // than a confident wrong guess.
    expect(d.label).toBe("SomeNewBrowser/1.0 (Fuchsia)")
    expect(d.kind).toBe("unknown")
  })

  it("truncates an absurdly long unrecognised agent so it cannot break the row", () => {
    const d = describeUserAgent("X".repeat(400))
    expect(d.label.length).toBeLessThanOrEqual(64)
    expect(d.label.endsWith("…")).toBe(true)
  })

  it("classifies form factor so the row can pick an icon", () => {
    expect(describeUserAgent("Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1").kind).toBe("mobile")
    expect(describeUserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36").kind).toBe("desktop")
  })
})
