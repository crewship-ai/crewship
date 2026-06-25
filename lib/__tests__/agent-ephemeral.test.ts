import { describe, it, expect } from "vitest"
import { isGhost, effectiveStatus, ttlRemaining, latestHireReason } from "../agent-ephemeral"

describe("agent-ephemeral helpers", () => {
  describe("isGhost", () => {
    it("is true only when expired_at is set", () => {
      expect(isGhost({ expired_at: "2026-06-25T10:00:00Z" })).toBe(true)
      expect(isGhost({ expired_at: null })).toBe(false)
      expect(isGhost({})).toBe(false)
    })
  })

  describe("effectiveStatus", () => {
    it("overrides any server status with EXPIRED once ghosted", () => {
      expect(effectiveStatus({ status: "RUNNING", expired_at: "2026-06-25T10:00:00Z" })).toBe("EXPIRED")
    })
    it("passes the server status through when live", () => {
      expect(effectiveStatus({ status: "PENDING_REVIEW", expired_at: null })).toBe("PENDING_REVIEW")
      expect(effectiveStatus({ status: "RUNNING" })).toBe("RUNNING")
    })
    it("defaults to IDLE when status is missing", () => {
      expect(effectiveStatus({})).toBe("IDLE")
    })
  })

  describe("ttlRemaining", () => {
    const now = Date.parse("2026-06-25T12:00:00Z")
    it("formats hours and minutes left", () => {
      expect(ttlRemaining("2026-06-25T15:12:00Z", now)).toBe("3h 12m left")
    })
    it("formats minutes-only when under an hour", () => {
      expect(ttlRemaining("2026-06-25T12:08:00Z", now)).toBe("8m left")
    })
    it("says 'expiring' once past the deadline", () => {
      expect(ttlRemaining("2026-06-25T11:59:00Z", now)).toBe("expiring")
    })
    it("shows '<1m left' for under a minute instead of rounding to 0m", () => {
      expect(ttlRemaining("2026-06-25T12:00:30Z", now)).toBe("<1m left")
    })
    it("returns empty for missing/unparseable expiry", () => {
      expect(ttlRemaining(null, now)).toBe("")
      expect(ttlRemaining(undefined, now)).toBe("")
      expect(ttlRemaining("not-a-date", now)).toBe("")
    })
  })

  describe("latestHireReason", () => {
    it("returns the last line, stripped of its timestamp prefix", () => {
      const log = "[2026-06-25T11:00:00Z] spike: rate-limiter\n[2026-06-25T13:00:00Z] rehire: keep going"
      expect(latestHireReason(log)).toBe("rehire: keep going")
    })
    it("handles a single bare reason", () => {
      expect(latestHireReason("one-off ETL")).toBe("one-off ETL")
    })
    it("returns empty for null/empty", () => {
      expect(latestHireReason(null)).toBe("")
      expect(latestHireReason("")).toBe("")
    })
  })
})
